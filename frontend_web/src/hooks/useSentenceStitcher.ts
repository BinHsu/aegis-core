// frontend_web/src/hooks/useSentenceStitcher.ts
//
// Accumulate consecutive `TranscriptSegment`s (projected through
// `TranscriptStreamProvider`'s `ViewerEvent` shape) into display lines
// that end on a sentence-closing punctuation mark. The engine emits one
// segment per `kLiveWindowSeconds` PCM flush (5 s as of PR #72) — some
// windows end mid-sentence, so the raw stream looks choppy ("臺灣是一個
// 位於" | "東亞的島嶼。"). This stitcher buffers until the buffer ends
// with `。 / ？ / ！ / . / ? / !` OR a timeout elapses, whichever comes
// first.
//
// Timeout path is load-bearing: a speaker who trails off without a
// sentence-ender should still see their words rendered within a few
// seconds — not held indefinitely waiting for punctuation that never
// arrives.
//
// Speaker change forces a flush: the pending buffer belongs to one
// speaker; a new speakerLabel starts a fresh line.
//
// Structured as a factory + a thin React hook wrapper so the core
// buffering logic is unit-testable without a React tree or a DOM.

import { useEffect, useRef } from "react";

export interface StitchedLine {
  /** Accumulated sentence (or sentence fragment on timeout). */
  readonly text: string;
  /** Speaker label carried from the first segment in the buffer. */
  readonly speaker: string;
  /** Any of the merged segments was marked isQuestion by the engine. */
  readonly isQuestion: boolean;
  /**
   * `true` if the line ends with a sentence-closing punctuation.
   * `false` if the timeout forced the flush (buffer may end mid-phrase).
   * UIs may render incomplete lines differently (e.g. trailing ellipsis)
   * if they care; `ViewerPage` currently treats both the same way.
   */
  readonly isComplete: boolean;
}

/** Matches the fields we read from a `ViewerEvent` of kind="transcript". */
export interface StitcherInput {
  readonly text: string;
  readonly speakerLabel: string;
  readonly isQuestion: boolean;
}

export interface SentenceStitcherOptions {
  /**
   * Maximum milliseconds to wait for a sentence-closing punctuation
   * before flushing the pending buffer as an incomplete line. Default
   * 4000 — matches the engine's 5 s window with ~1 s of slack so a
   * buffer accumulated from ONE window that never reaches a period
   * still renders within ~9 s of the speaker starting.
   */
  readonly timeoutMs?: number;
  /**
   * Characters that end a sentence. Default covers CJK + ASCII.
   * `，` (CJK comma) is intentionally NOT included — intra-sentence.
   */
  readonly sentenceEnders?: readonly string[];
  /**
   * Called when a line becomes ready to display. Fires zero or one
   * time per `pushSegment` call in the synchronous path; additional
   * fires happen on the timeout timer or on `destroy()`.
   */
  readonly onLine: (line: StitchedLine) => void;
}

export interface SentenceStitcher {
  /** Feed one transcript segment into the buffer. */
  pushSegment(segment: StitcherInput): void;
  /**
   * Release the pending timer and flush any pending buffer as an
   * incomplete line. Call from the React hook's unmount cleanup.
   */
  destroy(): void;
}

const DEFAULT_ENDERS = ["。", "？", "！", ".", "?", "!"] as const;

interface PendingBuffer {
  text: string;
  speaker: string;
  isQuestion: boolean;
}

/**
 * Pure factory — no React. Owns the pending buffer + timeout timer.
 * Unit-testable by calling `pushSegment` and asserting on `onLine`
 * invocations + advancing fake timers via `vi.useFakeTimers()`.
 */
export function createSentenceStitcher(
  opts: SentenceStitcherOptions,
): SentenceStitcher {
  const { onLine, timeoutMs = 4000, sentenceEnders = DEFAULT_ENDERS } = opts;

  let pending: PendingBuffer | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;

  const flush = (complete: boolean): void => {
    if (pending === null) return;
    onLine({
      text: pending.text,
      speaker: pending.speaker,
      isQuestion: pending.isQuestion,
      isComplete: complete,
    });
    pending = null;
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
  };

  return {
    pushSegment(segment: StitcherInput): void {
      const incoming = segment.text.trim();
      if (incoming.length === 0) return;

      if (pending !== null && pending.speaker !== segment.speakerLabel) {
        // Speaker change — flush pending as incomplete, start fresh.
        flush(false);
      }

      if (pending === null) {
        pending = {
          text: incoming,
          speaker: segment.speakerLabel,
          isQuestion: segment.isQuestion,
        };
      } else {
        pending = {
          text: joinSegments(pending.text, incoming),
          speaker: pending.speaker,
          isQuestion: pending.isQuestion || segment.isQuestion,
        };
      }

      const lastChar = pending.text.slice(-1);
      if (sentenceEnders.includes(lastChar)) {
        flush(true);
        return;
      }

      // Arm (or re-arm) the timeout.
      if (timer !== null) clearTimeout(timer);
      timer = setTimeout(() => {
        timer = null;
        flush(false);
      }, timeoutMs);
    },
    destroy(): void {
      if (timer !== null) {
        clearTimeout(timer);
        timer = null;
      }
      if (pending !== null) {
        // Flush as incomplete on teardown so consumers can render the
        // last partial line before the underlying subscription unwinds.
        flush(false);
      }
    },
  };
}

/**
 * React hook wrapper. Holds a single stitcher instance across the
 * caller's lifecycle; destroys it on unmount so no timer or partial
 * buffer leaks. The returned `pushSegment` has stable identity and
 * is safe to wire into a provider's `onEvent` handler.
 */
export function useSentenceStitcher(opts: SentenceStitcherOptions): {
  pushSegment: (segment: StitcherInput) => void;
} {
  // Keep opts in a ref so the stitcher's closure always sees the
  // latest callbacks without re-instantiating on every render.
  const optsRef = useRef(opts);
  optsRef.current = opts;

  const stitcherRef = useRef<SentenceStitcher | null>(null);
  if (stitcherRef.current === null) {
    stitcherRef.current = createSentenceStitcher({
      onLine: (line) => optsRef.current.onLine(line),
      ...(opts.timeoutMs !== undefined ? { timeoutMs: opts.timeoutMs } : {}),
      ...(opts.sentenceEnders !== undefined
        ? { sentenceEnders: opts.sentenceEnders }
        : {}),
    });
  }

  useEffect(() => {
    return () => {
      stitcherRef.current?.destroy();
      stitcherRef.current = null;
    };
  }, []);

  return {
    pushSegment: (segment: StitcherInput): void => {
      stitcherRef.current?.pushSegment(segment);
    },
  };
}

/**
 * Concat two transcript fragments. Adds a single ASCII space when both
 * the boundary chars are alphanumeric (Latin word boundary); no space
 * around CJK or existing whitespace/punctuation. Avoids the common
 * "theisland" glue at segment joins for English speech while keeping
 * zh-TW (where CJK chars abut without spaces) clean.
 */
export function joinSegments(left: string, right: string): string {
  if (left.length === 0) return right;
  if (right.length === 0) return left;
  const leftTail = left.slice(-1);
  const rightHead = right.slice(0, 1);
  const needsSpace = isLatinWordChar(leftTail) && isLatinWordChar(rightHead);
  return needsSpace ? `${left} ${right}` : `${left}${right}`;
}

function isLatinWordChar(ch: string): boolean {
  if (ch.length === 0) return false;
  const code = ch.charCodeAt(0);
  // ASCII letters + digits.
  return (
    (code >= 0x30 && code <= 0x39) || // 0-9
    (code >= 0x41 && code <= 0x5a) || // A-Z
    (code >= 0x61 && code <= 0x7a) // a-z
  );
}
