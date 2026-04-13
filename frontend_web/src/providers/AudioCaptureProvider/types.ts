// frontend_web/src/providers/AudioCaptureProvider/types.ts
//
// ADR-0002 Constraint 2 + ADR-0003: the AudioCaptureProvider interface
// isolates host-side audio capture so the UI never calls
// navigator.mediaDevices directly. Phase 1 C2 ships the Web
// implementation (getUserMedia + getDisplayMedia + Web Audio mixing);
// Phase 4+ Tauri adds a native CoreAudio / WASAPI implementation
// behind the same interface.

/**
 * Which audio source the host is capturing. Matches the three modes
 * described in ADR-0003 §"Capture Flow (MVP, Remote Meeting Example)".
 */
export type CaptureMode =
  /** Physical conference room — laptop microphone via getUserMedia. */
  | "microphone"
  /**
   * Remote meeting — capture a browser-tab audio stream from a web
   * meeting client (Zoom Web / Google Meet / Teams Web) via
   * getDisplayMedia. Video track is discarded immediately.
   */
  | "browser-tab"
  /** Mic + tab audio mixed via Web Audio API into a single stream. */
  | "microphone-and-tab";

export interface CaptureRequest {
  readonly mode: CaptureMode;
  /**
   * Hint for getUserMedia echoCancellation / noiseSuppression /
   * autoGainControl. Defaults to true when unspecified.
   */
  readonly audioConstraints?: Partial<{
    readonly echoCancellation: boolean;
    readonly noiseSuppression: boolean;
    readonly autoGainControl: boolean;
  }>;
}

export interface CaptureSession {
  /**
   * The mixed mono MediaStream ready to attach to
   * RTCPeerConnection.addTrack(). Single-channel per ADR-0003 §5
   * (single-channel diarization simplifies capture mechanics).
   */
  readonly stream: MediaStream;

  /** What mode produced the stream — useful for UI labels. */
  readonly mode: CaptureMode;

  /**
   * Tear down all underlying MediaStreams, AudioContext nodes, and
   * tracks. Idempotent — calling stop() twice is safe. Callers MUST
   * invoke this when the session ends to release the microphone /
   * tab-capture indicator.
   */
  stop(): Promise<void>;
}

/**
 * Reasons a capture attempt can fail. Map to user-facing UI copy
 * without exposing raw browser exceptions.
 */
export type CaptureErrorCode =
  | "permission-denied" // user denied prompt
  | "user-cancelled" // getDisplayMedia picker dismissed
  | "no-audio-track" // user shared a tab with no "Share tab audio"
  | "unsupported-browser" // getDisplayMedia not available
  | "internal";

export class CaptureError extends Error {
  readonly code: CaptureErrorCode;
  constructor(code: CaptureErrorCode, message: string) {
    super(message);
    this.code = code;
    this.name = "CaptureError";
  }
}

/**
 * The interface Phase 4+ Tauri and any future native shell must
 * implement. The UI depends ONLY on this surface.
 */
export interface AudioCaptureProvider {
  /**
   * Prompt the browser (or native) for the requested capture source
   * and return a live session. Implementations must normalize the
   * stream to mono 16 kHz before returning — whisper.cpp's canonical
   * input format — OR flag a sample-rate mismatch in a follow-up
   * error if the platform cannot provide that directly (the current
   * web implementation lets the engine resample on ingest, per
   * ADR-0003 §"Constraints and Caveats").
   */
  start(request: CaptureRequest): Promise<CaptureSession>;

  /**
   * Return true when the platform can satisfy the given capture mode.
   * The UI uses this to disable capture-mode radio buttons the user
   * cannot select (e.g., Firefox on browser-tab mode in MVP).
   */
  isSupported(mode: CaptureMode): boolean;
}
