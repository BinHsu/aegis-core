// frontend_web/src/hooks/useSentenceStitcher.test.ts
//
// Unit coverage for `createSentenceStitcher` — the factory behind
// `useSentenceStitcher`. Tests the pure logic (no React / DOM) so the
// React wrapper stays a thin adapter; every invariant caller-visible
// via the hook is covered here.
//
// Tested invariants:
//  1. A single segment with no sentence-ender stays buffered (zero
//     emissions synchronously).
//  2. A segment ending with `。` fires exactly one emission with
//     isComplete=true and correct text.
//  3. Multi-segment accumulation merges into one line when the last
//     segment completes the sentence.
//  4. Timeout fires when a buffer sits without punctuation long enough
//     — emission has isComplete=false.
//  5. Speaker change flushes the pending buffer as incomplete and
//     starts a fresh buffer for the new speaker.
//  6. destroy() flushes any pending buffer as incomplete and does not
//     leak the timeout timer.
//  7. ASCII and CJK sentence enders both work; `，` (CJK comma) does
//     NOT end a sentence.
//  8. joinSegments: space between Latin word chars, no space around
//     CJK or existing whitespace.

import { describe, test, expect, beforeEach, afterEach, vi } from "vitest";
import {
  createSentenceStitcher,
  joinSegments,
  type StitchedLine,
} from "./useSentenceStitcher";

function seg(
  text: string,
  speakerLabel = "Speaker_0",
  isQuestion = false,
): { text: string; speakerLabel: string; isQuestion: boolean } {
  return { text, speakerLabel, isQuestion };
}

describe("createSentenceStitcher", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  test("buffers a partial segment without emitting", () => {
    const lines: StitchedLine[] = [];
    const s = createSentenceStitcher({ onLine: (l) => lines.push(l) });
    s.pushSegment(seg("臺灣是一個"));
    expect(lines).toHaveLength(0);
  });

  test("emits a complete line when the segment ends with 。", () => {
    const lines: StitchedLine[] = [];
    const s = createSentenceStitcher({ onLine: (l) => lines.push(l) });
    s.pushSegment(seg("臺灣是一個位於東亞的島嶼。"));
    expect(lines).toHaveLength(1);
    expect(lines[0]).toMatchObject({
      text: "臺灣是一個位於東亞的島嶼。",
      speaker: "Speaker_0",
      isComplete: true,
    });
  });

  test("merges multi-segment input into one line on final sentence-end", () => {
    const lines: StitchedLine[] = [];
    const s = createSentenceStitcher({ onLine: (l) => lines.push(l) });
    s.pushSegment(seg("臺灣是一個"));
    s.pushSegment(seg("位於東亞的"));
    s.pushSegment(seg("島嶼。"));
    expect(lines).toHaveLength(1);
    expect(lines[0]?.text).toBe("臺灣是一個位於東亞的島嶼。");
    expect(lines[0]?.isComplete).toBe(true);
  });

  test("timeout flushes pending buffer as incomplete", () => {
    const lines: StitchedLine[] = [];
    const s = createSentenceStitcher({
      onLine: (l) => lines.push(l),
      timeoutMs: 2000,
    });
    s.pushSegment(seg("臺灣是一個位於"));
    // Under the timeout — no emission yet.
    vi.advanceTimersByTime(1500);
    expect(lines).toHaveLength(0);
    // Past the timeout — one incomplete emission.
    vi.advanceTimersByTime(600);
    expect(lines).toHaveLength(1);
    expect(lines[0]?.isComplete).toBe(false);
    expect(lines[0]?.text).toBe("臺灣是一個位於");
  });

  test("each incoming segment re-arms the timeout", () => {
    const lines: StitchedLine[] = [];
    const s = createSentenceStitcher({
      onLine: (l) => lines.push(l),
      timeoutMs: 2000,
    });
    s.pushSegment(seg("臺灣"));
    vi.advanceTimersByTime(1500);
    s.pushSegment(seg("是一個"));
    // The first timer was cancelled; a fresh 2s window started.
    vi.advanceTimersByTime(1500);
    expect(lines).toHaveLength(0);
    // Now past the re-armed deadline.
    vi.advanceTimersByTime(600);
    expect(lines).toHaveLength(1);
    expect(lines[0]?.text).toBe("臺灣是一個");
    expect(lines[0]?.isComplete).toBe(false);
  });

  test("speaker change flushes pending as incomplete and starts a new buffer", () => {
    const lines: StitchedLine[] = [];
    const s = createSentenceStitcher({ onLine: (l) => lines.push(l) });
    s.pushSegment(seg("臺灣是一個", "Speaker_0"));
    s.pushSegment(seg("Hello world.", "Speaker_1"));
    expect(lines).toHaveLength(2);
    expect(lines[0]).toMatchObject({
      text: "臺灣是一個",
      speaker: "Speaker_0",
      isComplete: false,
    });
    expect(lines[1]).toMatchObject({
      text: "Hello world.",
      speaker: "Speaker_1",
      isComplete: true,
    });
  });

  test("destroy flushes pending buffer as incomplete and clears timer", () => {
    const lines: StitchedLine[] = [];
    const s = createSentenceStitcher({
      onLine: (l) => lines.push(l),
      timeoutMs: 10_000,
    });
    s.pushSegment(seg("臺灣是一個"));
    s.destroy();
    expect(lines).toHaveLength(1);
    expect(lines[0]?.isComplete).toBe(false);
    // After destroy, advancing time must NOT re-fire the flush.
    vi.advanceTimersByTime(20_000);
    expect(lines).toHaveLength(1);
  });

  test("ASCII sentence enders work (. ? !)", () => {
    const lines: StitchedLine[] = [];
    const s = createSentenceStitcher({ onLine: (l) => lines.push(l) });
    s.pushSegment(seg("Hello world."));
    s.pushSegment(seg("What about this?", "Speaker_1"));
    s.pushSegment(seg("Amazing!", "Speaker_2"));
    expect(lines.map((l) => l.text)).toEqual([
      "Hello world.",
      "What about this?",
      "Amazing!",
    ]);
    expect(lines.every((l) => l.isComplete)).toBe(true);
  });

  test("CJK comma is NOT a sentence ender", () => {
    const lines: StitchedLine[] = [];
    const s = createSentenceStitcher({
      onLine: (l) => lines.push(l),
      timeoutMs: 5000,
    });
    s.pushSegment(seg("臺灣，"));
    // No sentence ender yet.
    expect(lines).toHaveLength(0);
    s.pushSegment(seg("是一個美麗的島嶼。"));
    expect(lines).toHaveLength(1);
    expect(lines[0]?.text).toBe("臺灣，是一個美麗的島嶼。");
  });

  test("empty / whitespace-only segments are ignored", () => {
    const lines: StitchedLine[] = [];
    const s = createSentenceStitcher({ onLine: (l) => lines.push(l) });
    s.pushSegment(seg(""));
    s.pushSegment(seg("   "));
    s.pushSegment(seg("real text"));
    vi.advanceTimersByTime(5000);
    expect(lines).toHaveLength(1);
    expect(lines[0]?.text).toBe("real text");
    expect(lines[0]?.isComplete).toBe(false);
  });

  test("isQuestion sticks across merged segments", () => {
    const lines: StitchedLine[] = [];
    const s = createSentenceStitcher({ onLine: (l) => lines.push(l) });
    s.pushSegment(seg("what is", "Speaker_0", false));
    s.pushSegment(seg("the weather", "Speaker_0", true));
    s.pushSegment(seg("like?", "Speaker_0", false));
    expect(lines).toHaveLength(1);
    expect(lines[0]?.isQuestion).toBe(true);
  });
});

describe("joinSegments", () => {
  test("adds a space between Latin word characters", () => {
    expect(joinSegments("hello", "world")).toBe("hello world");
    expect(joinSegments("foo123", "bar")).toBe("foo123 bar");
  });

  test("no space around CJK", () => {
    expect(joinSegments("臺灣", "島嶼")).toBe("臺灣島嶼");
    expect(joinSegments("臺灣", "is")).toBe("臺灣is");
    expect(joinSegments("the", "島嶼")).toBe("the島嶼");
  });

  test("no space when boundary is punctuation or whitespace", () => {
    expect(joinSegments("hello.", "World")).toBe("hello.World");
    expect(joinSegments("hello ", "world")).toBe("hello world");
    expect(joinSegments("臺灣。", "這裡")).toBe("臺灣。這裡");
  });

  test("handles empty inputs", () => {
    expect(joinSegments("", "hello")).toBe("hello");
    expect(joinSegments("hello", "")).toBe("hello");
    expect(joinSegments("", "")).toBe("");
  });
});
