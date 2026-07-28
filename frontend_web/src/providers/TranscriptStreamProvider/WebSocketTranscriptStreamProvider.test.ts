// frontend_web/src/providers/TranscriptStreamProvider/WebSocketTranscriptStreamProvider.test.ts
//
// Regression tests for Incident 14 (2026-04-20). The original
// `WebSocketTranscriptStreamProvider` carried a Phase-1 stub that only
// handled string WebSocket frames and silently dropped binary frames.
// The Go Gateway sends `aegis.v1.ViewerEvent` as binary protobuf via
// `websocket.MessageBinary`, so in LAN mode the host UI received
// perfectly-flowing transcripts from the gateway but rendered nothing.
//
// These tests assert the contract that a refreshed decoder must hold:
//
//  1. Binary frames encoding a real ViewerEvent are parsed and
//     forwarded through `onEvent` with the correct shape.
//  2. Unknown / empty payload oneofs surface as no-event (not as
//     a crash).
//  3. Malformed binary triggers `onError` rather than silent drop.
//  4. Stray string frames are surfaced via `onError` (they indicate
//     a gateway protocol bug; must not be silently consumed).
//  5. `unsubscribe()` is idempotent and closes the socket.
//
// The test substitutes a `FakeWebSocket` for the real `WebSocket`
// global — Vitest provides no built-in WS mock, and the alternative
// (intercepting via `vi.stubGlobal`) achieves the same result with
// more boilerplate. The fake fires lifecycle events in the order a
// real browser socket would.

import { describe, test, expect, beforeEach, afterEach } from "vitest";

// protobuf-es v2: `create(FooSchema)` replaces `new Foo()` and
// `toBinary(FooSchema, msg)` replaces `msg.toBinary()`. Messages are
// plain objects now, so the schema constant has to travel with them.
import { create, toBinary } from "@bufbuild/protobuf";
// google.protobuf.Timestamp is a well-known type, so its schema comes from
// the runtime's wkt entrypoint rather than from aegis_pb.ts.
import { TimestampSchema } from "@bufbuild/protobuf/wkt";

import {
  HintUrgency as ProtoHintUrgency,
  MeetingState as ProtoMeetingState,
  MeetingStateChangeSchema,
  PrompterHintSchema,
  TranscriptSegmentSchema,
  ViewerEventSchema,
  type ViewerEvent as ProtoViewerEvent,
} from "@/gen/proto/aegis/v1/aegis_pb";

import { WebSocketTranscriptStreamProvider } from "./WebSocketTranscriptStreamProvider";
import type { ViewerEvent } from "./types";

// ---- Test double: enough of the WebSocket surface for this provider ----

interface FakeWebSocketInstance {
  readonly url: string;
  binaryType: BinaryType;
  onmessage: ((ev: MessageEvent) => void) | null;
  onerror: ((ev: Event) => void) | null;
  onclose: ((ev: CloseEvent) => void) | null;
  readyState: number;
  close(code?: number, reason?: string): void;
  // Test-only helpers — not on the real WebSocket interface.
  _emitBinary(buf: ArrayBuffer): void;
  _emitString(s: string): void;
  _emitError(): void;
  _emitClose(code: number, reason: string): void;
}

let lastSocket: FakeWebSocketInstance | null = null;

class FakeWebSocket implements FakeWebSocketInstance {
  public binaryType: BinaryType = "blob";
  public onmessage: ((ev: MessageEvent) => void) | null = null;
  public onerror: ((ev: Event) => void) | null = null;
  public onclose: ((ev: CloseEvent) => void) | null = null;
  public readyState = 1; // OPEN
  constructor(public readonly url: string) {
    lastSocket = this;
  }
  close(code = 1000, reason = ""): void {
    this.readyState = 3; // CLOSED
    if (this.onclose) {
      this.onclose(new CloseEvent("close", { code, reason }));
    }
  }
  _emitBinary(buf: ArrayBuffer): void {
    if (!this.onmessage) return;
    this.onmessage(new MessageEvent("message", { data: buf }));
  }
  _emitString(s: string): void {
    if (!this.onmessage) return;
    this.onmessage(new MessageEvent("message", { data: s }));
  }
  _emitError(): void {
    if (this.onerror) {
      this.onerror(new Event("error"));
    }
  }
  _emitClose(code: number, reason: string): void {
    if (this.onclose) {
      this.onclose(new CloseEvent("close", { code, reason }));
    }
  }
}

// Stash and swap the real WebSocket during each test.
const realWebSocket = globalThis.WebSocket;

beforeEach(() => {
  lastSocket = null;
  (globalThis as { WebSocket: typeof WebSocket }).WebSocket =
    FakeWebSocket as unknown as typeof WebSocket;
});

afterEach(() => {
  (globalThis as { WebSocket: typeof WebSocket }).WebSocket = realWebSocket;
});

// ---- Helpers ----

function encodeViewerEvent(build: (ve: ProtoViewerEvent) => void): ArrayBuffer {
  const ve = create(ViewerEventSchema);
  build(ve);
  const bytes = toBinary(ViewerEventSchema, ve);
  // The provider expects an ArrayBuffer; WebSocket frames with
  // `binaryType = "arraybuffer"` arrive as such. Make sure the
  // underlying buffer is exactly the right size (toBinary() returns
  // a Uint8Array that can share a larger backing buffer via pool
  // allocators).
  // TS 5.7's lib types Uint8Array.buffer as ArrayBuffer | SharedArrayBuffer;
  // a WebSocket "arraybuffer" frame is always a plain ArrayBuffer. Assert it
  // so .slice() returns ArrayBuffer (pre-existing, unrelated to the config
  // refactor — surfaced by a fresh dependency install with no lockfile).
  return (bytes.buffer as ArrayBuffer).slice(
    bytes.byteOffset,
    bytes.byteOffset + bytes.byteLength,
  );
}

function newProvider(): WebSocketTranscriptStreamProvider {
  return new WebSocketTranscriptStreamProvider({
    endpoint: "http://localhost:8080",
  });
}

const REQUEST = { sessionId: "test-session-42", viewerToken: "tok" };

// ---- Tests ----

describe("WebSocketTranscriptStreamProvider — binary frame decoding (Incident 14 regression)", () => {
  test("transcript binary frame → onEvent with kind=transcript and correct fields", () => {
    const events: ViewerEvent[] = [];
    const provider = newProvider();
    provider.subscribe(REQUEST, { onEvent: (ev) => events.push(ev) });

    const frame = encodeViewerEvent((ve) => {
      ve.sequence = 7n;
      const t = create(TranscriptSegmentSchema);
      t.segmentId = 3n;
      t.speakerLabel = "Speaker_0";
      t.text = "ask not what your country can do for you";
      t.isFinal = true;
      t.isQuestion = false;
      ve.payload = { case: "transcript", value: t };
    });
    lastSocket!._emitBinary(frame);

    expect(events).toHaveLength(1);
    const got = events[0];
    expect(got.kind).toBe("transcript");
    if (got.kind === "transcript") {
      expect(got.sequence).toBe(7);
      expect(got.segmentId).toBe(3);
      expect(got.speakerLabel).toBe("Speaker_0");
      expect(got.text).toBe("ask not what your country can do for you");
      expect(got.isFinal).toBe(true);
      expect(got.isQuestion).toBe(false);
    }
  });

  test("hint binary frame → onEvent with kind=hint and mapped urgency", () => {
    const events: ViewerEvent[] = [];
    const provider = newProvider();
    provider.subscribe(REQUEST, { onEvent: (ev) => events.push(ev) });

    const frame = encodeViewerEvent((ve) => {
      const h = create(PrompterHintSchema);
      h.hintId = 12n;
      h.suggestion = "Taiwan's population is ~23 million.";
      h.urgency = ProtoHintUrgency.HIGH;
      ve.payload = { case: "hint", value: h };
    });
    lastSocket!._emitBinary(frame);

    expect(events).toHaveLength(1);
    const got = events[0];
    expect(got.kind).toBe("hint");
    if (got.kind === "hint") {
      expect(got.urgency).toBe("high");
      expect(got.suggestion).toMatch(/Taiwan/);
    }
  });

  test("state change binary frame → onEvent with kind=state", () => {
    const events: ViewerEvent[] = [];
    const provider = newProvider();
    provider.subscribe(REQUEST, { onEvent: (ev) => events.push(ev) });

    const frame = encodeViewerEvent((ve) => {
      const s = create(MeetingStateChangeSchema);
      s.state = ProtoMeetingState.ACTIVE;
      s.reason = "joined";
      ve.payload = { case: "stateChange", value: s };
    });
    lastSocket!._emitBinary(frame);

    expect(events).toHaveLength(1);
    const got = events[0];
    expect(got.kind).toBe("state");
    if (got.kind === "state") {
      expect(got.state).toBe("active");
      expect(got.reason).toBe("joined");
    }
  });

  test("empty-payload frame → no onEvent (not a crash)", () => {
    const events: ViewerEvent[] = [];
    const errors: Error[] = [];
    const provider = newProvider();
    provider.subscribe(REQUEST, {
      onEvent: (ev) => events.push(ev),
      onError: (e) => errors.push(e),
    });

    // ViewerEvent with payload.case === undefined
    const frame = encodeViewerEvent(() => {
      /* leave payload unset */
    });
    lastSocket!._emitBinary(frame);

    expect(events).toHaveLength(0);
    expect(errors).toHaveLength(0);
  });

  test("malformed binary → onError (not silent drop — this was the Incident 14 shape)", () => {
    const events: ViewerEvent[] = [];
    const errors: Error[] = [];
    const provider = newProvider();
    provider.subscribe(REQUEST, {
      onEvent: (ev) => events.push(ev),
      onError: (e) => errors.push(e),
    });

    // Random bytes unlikely to parse as a well-formed ViewerEvent.
    const bad = new Uint8Array([0xff, 0xff, 0xff, 0xff, 0xff, 0xff]);
    lastSocket!._emitBinary(
      bad.buffer.slice(bad.byteOffset, bad.byteOffset + bad.byteLength),
    );

    expect(events).toHaveLength(0);
    expect(errors.length).toBeGreaterThanOrEqual(1);
    expect(errors[0].message).toMatch(/decode/i);
  });

  test("string frame → onError (gateway contract violation must be surfaced, not dropped — direct Incident 14 regression)", () => {
    const events: ViewerEvent[] = [];
    const errors: Error[] = [];
    const provider = newProvider();
    provider.subscribe(REQUEST, {
      onEvent: (ev) => events.push(ev),
      onError: (e) => errors.push(e),
    });

    lastSocket!._emitString("hello text frame");

    expect(events).toHaveLength(0);
    expect(errors).toHaveLength(1);
    expect(errors[0].message).toMatch(/text frame/);
  });

  test("unsubscribe() closes socket and is idempotent", () => {
    const provider = newProvider();
    const sub = provider.subscribe(REQUEST, { onEvent: () => undefined });

    expect(lastSocket!.readyState).toBe(1);
    sub.unsubscribe();
    expect(lastSocket!.readyState).toBe(3);
    // Second call must not throw.
    sub.unsubscribe();
    expect(lastSocket!.readyState).toBe(3);
  });

  test("ws.onclose forwarded through onClose callback", () => {
    let closeReason: string | undefined;
    const provider = newProvider();
    provider.subscribe(REQUEST, {
      onEvent: () => undefined,
      onClose: (reason) => {
        closeReason = reason;
      },
    });

    lastSocket!._emitClose(1006, "abnormal");

    expect(closeReason).toMatch(/code=1006/);
    expect(closeReason).toMatch(/abnormal/);
  });

  test("sets binaryType=arraybuffer so browser delivers ArrayBuffer (not Blob) — precondition for the decoder", () => {
    const provider = newProvider();
    provider.subscribe(REQUEST, { onEvent: () => undefined });
    expect(lastSocket!.binaryType).toBe("arraybuffer");
  });

  test("builds the WS URL with http→ws scheme swap and url-encoded token", () => {
    const provider = newProvider();
    provider.subscribe(
      { sessionId: "abc", viewerToken: "t&k=x?y" },
      { onEvent: () => undefined },
    );
    // http:// → ws://
    expect(lastSocket!.url.startsWith("ws://")).toBe(true);
    // `&`, `=`, `?` in the token are encoded.
    expect(lastSocket!.url).toContain("token=t%26k%3Dx%3Fy");
    expect(lastSocket!.url).toContain("session_id=abc");
    expect(
      lastSocket!.url.endsWith("/ws/viewer?session_id=abc&token=t%26k%3Dx%3Fy"),
    ).toBe(true);
  });
});

// ---- 64-bit conversion boundaries ----
//
// The provider's `bigintToNumber` clamps any protobuf uint64 / int64
// above Number.MAX_SAFE_INTEGER down to Number.MAX_SAFE_INTEGER, rather
// than returning a silently-lossy Number, NaN, or throwing. That is a
// real boundary B = 9007199254740991, and every field routed through the
// clamp is exercised below at B-1, B and B+1:
//
//   ViewerEvent.sequence            (uint64)
//   ViewerEvent.emitted_at.seconds  (int64, via timestampToMs)
//   TranscriptSegment.segment_id    (uint64)
//   PrompterHint.hint_id            (uint64)
//
// Every case builds a real wire-encoded ViewerEvent and pushes it through
// the provider's own decoder, so each assertion covers the varint
// round-trip and the clamp together. Nothing here asserts on a spy.
//
// Expected values are decimal literals, not expressions over
// Number.MAX_SAFE_INTEGER. If the clamp constant changes, these fail
// instead of moving with it.

const B_MINUS_1 = 9007199254740990n;
const B = 9007199254740991n; // Number.MAX_SAFE_INTEGER
const B_PLUS_1 = 9007199254740992n;

/**
 * Encode one ViewerEvent, push it through a live provider, and return the
 * single decoded event. Fails loudly if the frame produced a decode error
 * or no event at all — otherwise a regression in the decoder would make
 * every field assertion below vacuously true.
 */
function decodeOneFrame(build: (ve: ProtoViewerEvent) => void): ViewerEvent {
  const events: ViewerEvent[] = [];
  const errors: Error[] = [];
  const provider = newProvider();
  provider.subscribe(REQUEST, {
    onEvent: (ev) => events.push(ev),
    onError: (e) => errors.push(e),
  });
  lastSocket!._emitBinary(encodeViewerEvent(build));
  expect(errors).toEqual([]);
  expect(events).toHaveLength(1);
  return events[0];
}

// `sequence` and `emitted_at` live on ViewerEvent, but the decoder returns
// null unless the payload oneof is set, so boundary frames for those two
// fields still need a payload attached.
function withTranscriptPayload(ve: ProtoViewerEvent): void {
  ve.payload = {
    case: "transcript",
    value: create(TranscriptSegmentSchema),
  };
}

// [label, value on the wire, expected decoded number, value that would
// appear if the clamp were missing — null where no clamp applies]
type BoundaryRow = readonly [string, bigint, number, number | null];

// Shared by all three uint64 fields — sequence, segment_id and hint_id
// have the same domain and the same clamp, so they have the same
// expectations. emitted_at.seconds gets its own rows further down
// because timestampToMs scales the value by 1000.
const UINT64_CLAMP_ROWS: readonly BoundaryRow[] = [
  ["B-1", B_MINUS_1, 9007199254740990, null],
  ["B", B, 9007199254740991, null],
  ["B+1", B_PLUS_1, 9007199254740991, 9007199254740992],
];

describe("WebSocketTranscriptStreamProvider — 64-bit conversion boundaries", () => {
  test("the literal these expectations are written against is Number.MAX_SAFE_INTEGER", () => {
    expect(9007199254740991).toBe(Number.MAX_SAFE_INTEGER);
    expect(B).toBe(BigInt(Number.MAX_SAFE_INTEGER));
  });

  test.each(UINT64_CLAMP_ROWS)(
    "ViewerEvent.sequence at %s (wire %d) decodes to %d",
    (_label, wire, expected, unclamped) => {
      const got = decodeOneFrame((ve) => {
        ve.sequence = wire;
        withTranscriptPayload(ve);
      });
      expect(got.sequence).toBe(expected);
      if (unclamped !== null) {
        expect(got.sequence).not.toBe(unclamped);
      }
    },
  );

  test.each(UINT64_CLAMP_ROWS)(
    "TranscriptSegment.segment_id at %s (wire %d) decodes to %d",
    (_label, wire, expected, unclamped) => {
      const got = decodeOneFrame((ve) => {
        const t = create(TranscriptSegmentSchema);
        t.segmentId = wire;
        ve.payload = { case: "transcript", value: t };
      });
      if (got.kind !== "transcript") {
        throw new Error(`expected kind=transcript, got kind=${got.kind}`);
      }
      expect(got.segmentId).toBe(expected);
      if (unclamped !== null) {
        expect(got.segmentId).not.toBe(unclamped);
      }
    },
  );

  test.each(UINT64_CLAMP_ROWS)(
    "PrompterHint.hint_id at %s (wire %d) decodes to %d",
    (_label, wire, expected, unclamped) => {
      const got = decodeOneFrame((ve) => {
        const h = create(PrompterHintSchema);
        h.hintId = wire;
        h.suggestion = "boundary probe";
        ve.payload = { case: "hint", value: h };
      });
      if (got.kind !== "hint") {
        throw new Error(`expected kind=hint, got kind=${got.kind}`);
      }
      expect(got.hintId).toBe(expected);
      if (unclamped !== null) {
        expect(got.hintId).not.toBe(unclamped);
      }
    },
  );

  // timestampToMs multiplies the clamped seconds by 1000, so the three
  // boundary cases land 1000 apart in a region where consecutive doubles
  // are ~2000 apart. They are still three distinct doubles (verified: the
  // literals below are exact), so the boundary remains observable — but
  // see the nanos test after this one for what stops being observable.
  test.each([
    ["B-1", B_MINUS_1, 9007199254740990000, null],
    ["B", B, 9007199254740991000, null],
    ["B+1", B_PLUS_1, 9007199254740991000, 9007199254740992000],
  ] as readonly BoundaryRow[])(
    "ViewerEvent.emitted_at.seconds at %s (wire %d) decodes to %d ms",
    (_label, wire, expected, unclamped) => {
      const got = decodeOneFrame((ve) => {
        ve.emittedAt = create(TimestampSchema, { seconds: wire, nanos: 0 });
        withTranscriptPayload(ve);
      });
      expect(got.emittedAtMs).toBe(expected);
      if (unclamped !== null) {
        expect(got.emittedAtMs).not.toBe(unclamped);
      }
    },
  );

  // Second boundary inside timestampToMs, at seconds = B. Adjacent doubles
  // near 9.007e18 are 1024 apart, so the `+ Math.floor(nanos / 1e6)` term
  // cannot land anywhere in between — it either rounds away entirely or
  // jumps a full 1024 ms step. The changeover is exactly half a step, at
  // 512 ms, which is a real boundary and gets its own B-1 / B / B+1 sweep.
  //
  // This block originally asserted that nanos was absorbed entirely at
  // this magnitude. That was wrong: 512 ms and above rounds up. The tests
  // below are the measured behaviour.
  test.each([
    ["511 ms (below the half-step)", 511_000_000, 9007199254740991000],
    ["512 ms (the half-step itself)", 512_000_000, 9007199254740992000],
    ["513 ms (above the half-step)", 513_000_000, 9007199254740992000],
  ] as readonly (readonly [string, number, number])[])(
    "at seconds = B, nanos = %s (%d ns) rounds emittedAtMs to %d",
    (_label, nanos, expected) => {
      const got = decodeOneFrame((ve) => {
        ve.emittedAt = create(TimestampSchema, { seconds: B, nanos });
        withTranscriptPayload(ve);
      });
      expect(got.emittedAtMs).toBe(expected);
    },
  );

  // NAMED LIMITATION — recorded, not hidden. Because of that 1024 ms
  // rounding step, a CLAMPED timestamp (seconds = B, nanos >= 512 ms)
  // produces exactly the same emittedAtMs as an UNCLAMPED seconds = B+1
  // with nanos = 0 would. So emittedAtMs alone cannot distinguish "the
  // clamp fired" from "the value overflowed" once nanos is large enough
  // to round up. The clamp is still verified unambiguously by the
  // nanos = 0 sweep above, and by the sequence / segment_id / hint_id
  // sweeps, which have no multiplication and therefore no aliasing.
  // Flagged for a decision on timestampToMs rather than papered over —
  // see PR #193.
  test("at seconds = B with nanos >= 512 ms, emittedAtMs aliases an unclamped B+1 (known aliasing)", () => {
    const clampedWithNanos = decodeOneFrame((ve) => {
      ve.emittedAt = create(TimestampSchema, {
        seconds: B,
        nanos: 999_999_999,
      });
      withTranscriptPayload(ve);
    });
    const unclampedNextSecond = 9007199254740992 * 1000;
    expect(clampedWithNanos.emittedAtMs).toBe(unclampedNextSecond);
    // The aliasing is a property of the *1000 multiply, so it must not be
    // read as the clamp failing: the seconds value itself was clamped.
    expect(clampedWithNanos.emittedAtMs).not.toBe(
      9007199254740992 * 1000 + 1024,
    );
  });

  // NAMED GAP — the clamp is one-sided. `bigintToNumber` guards only the
  // upper bound, so google.protobuf.Timestamp's signed `seconds` has no
  // lower clamp at -Number.MAX_SAFE_INTEGER. A B-1 / B / B+1 sweep on the
  // negative side would therefore assert lossy pass-through, which would
  // lock in behaviour that has never been reviewed as intended. Flagged
  // for a decision rather than tested; see PR #193.
  test("zero is the natural lower bound for the unsigned fields", () => {
    const got = decodeOneFrame((ve) => {
      ve.sequence = 0n;
      const t = create(TranscriptSegmentSchema);
      t.segmentId = 0n;
      ve.payload = { case: "transcript", value: t };
    });
    if (got.kind !== "transcript") {
      throw new Error(`expected kind=transcript, got kind=${got.kind}`);
    }
    expect(got.sequence).toBe(0);
    expect(got.segmentId).toBe(0);
  });
});
