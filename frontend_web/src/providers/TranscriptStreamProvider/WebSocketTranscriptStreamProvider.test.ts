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

import {
  HintUrgency as ProtoHintUrgency,
  MeetingState as ProtoMeetingState,
  MeetingStateChange,
  PrompterHint,
  TranscriptSegment,
  ViewerEvent as ProtoViewerEvent,
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
  const ve = new ProtoViewerEvent();
  build(ve);
  const bytes = ve.toBinary();
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
      const t = new TranscriptSegment();
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
      const h = new PrompterHint();
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
      const s = new MeetingStateChange();
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
