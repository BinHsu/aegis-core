// frontend_web/src/providers/AudioCaptureProvider/WebAudioCaptureProvider.ts
//
// Browser implementation of AudioCaptureProvider per ADR-0003.
// Supported scenarios:
//
//   mode = "microphone":
//     getUserMedia({audio: ...})  →  MediaStream (single audio track)
//
//   mode = "browser-tab":
//     getDisplayMedia({video, audio})  →  MediaStream
//     → drop the video track immediately (explicit privacy copy
//       required in UI; per ADR-0003 §"Discarding getDisplayMedia
//       video track user anxiety")
//     → keep the audio track
//
//   mode = "microphone-and-tab":
//     both of the above → AudioContext mixing via
//     MediaStreamAudioSourceNode + MediaStreamAudioDestinationNode
//     → single MediaStream with one audio track
//
// L6 (README): host role supported only on Chrome and Edge for MVP;
// isSupported("browser-tab") returns false in Firefox / Safari where
// getDisplayMedia audio is flaky.

import type {
  AudioCaptureProvider,
  CaptureMode,
  CaptureRequest,
  CaptureSession,
} from "./types";
import { CaptureError } from "./types";

type DisplayMediaConstraints = {
  readonly video: boolean | MediaTrackConstraints;
  readonly audio: boolean | MediaTrackConstraints;
};

function hasGetDisplayMedia(): boolean {
  if (typeof navigator === "undefined") return false;
  const md = navigator.mediaDevices as MediaDevices & {
    getDisplayMedia?: (c: DisplayMediaConstraints) => Promise<MediaStream>;
  };
  return typeof md?.getDisplayMedia === "function";
}

function hasGetUserMedia(): boolean {
  if (typeof navigator === "undefined") return false;
  return typeof navigator.mediaDevices?.getUserMedia === "function";
}

function audioConstraintsFromRequest(
  req: CaptureRequest,
): MediaTrackConstraints {
  const audio = req.audioConstraints ?? {};
  return {
    echoCancellation: audio.echoCancellation ?? true,
    noiseSuppression: audio.noiseSuppression ?? true,
    autoGainControl: audio.autoGainControl ?? true,
    channelCount: 1,
  };
}

function translateGetMediaError(err: unknown): CaptureError {
  if (err instanceof DOMException) {
    switch (err.name) {
      case "NotAllowedError":
      case "SecurityError":
        return new CaptureError("permission-denied", err.message);
      case "AbortError":
      case "NotReadableError":
        return new CaptureError("user-cancelled", err.message);
      default:
        return new CaptureError("internal", `${err.name}: ${err.message}`);
    }
  }
  if (err instanceof Error) {
    return new CaptureError("internal", err.message);
  }
  return new CaptureError("internal", String(err));
}

async function startMicrophone(req: CaptureRequest): Promise<CaptureSession> {
  try {
    const stream = await navigator.mediaDevices.getUserMedia({
      audio: audioConstraintsFromRequest(req),
      video: false,
    });
    return sessionFromSingleStream(stream, "microphone");
  } catch (e) {
    throw translateGetMediaError(e);
  }
}

async function startBrowserTab(): Promise<CaptureSession> {
  if (!hasGetDisplayMedia()) {
    throw new CaptureError(
      "unsupported-browser",
      "getDisplayMedia not available; MVP supports Chrome/Edge only",
    );
  }
  try {
    const stream = await navigator.mediaDevices.getDisplayMedia({
      video: true,
      audio: true,
    });

    // Drop the video track immediately — UI copy must have already
    // told the user we are not recording screen contents (ADR-0003
    // §"Discarding getDisplayMedia video track user anxiety").
    for (const track of stream.getVideoTracks()) {
      track.stop();
      stream.removeTrack(track);
    }

    if (stream.getAudioTracks().length === 0) {
      for (const track of stream.getTracks()) track.stop();
      throw new CaptureError(
        "no-audio-track",
        'No audio track captured. Did you check "Share tab audio" in the tab picker?',
      );
    }
    return sessionFromSingleStream(stream, "browser-tab");
  } catch (e) {
    throw translateGetMediaError(e);
  }
}

async function startMicrophoneAndTab(
  req: CaptureRequest,
): Promise<CaptureSession> {
  const mic = await startMicrophone(req);
  let tab: CaptureSession;
  try {
    tab = await startBrowserTab();
  } catch (e) {
    await mic.stop();
    throw e;
  }

  // Mix via Web Audio. AudioContext lifetime must outlive the
  // returned stream — we retain it on the session and close it on
  // stop().
  const ctx = new AudioContext();
  const micSource = ctx.createMediaStreamSource(mic.stream);
  const tabSource = ctx.createMediaStreamSource(tab.stream);
  const destination = ctx.createMediaStreamDestination();
  micSource.connect(destination);
  tabSource.connect(destination);

  const stream = destination.stream;

  let stopped = false;
  const stop = async (): Promise<void> => {
    if (stopped) return;
    stopped = true;
    try {
      micSource.disconnect();
    } catch {
      /* ignore */
    }
    try {
      tabSource.disconnect();
    } catch {
      /* ignore */
    }
    try {
      destination.disconnect();
    } catch {
      /* ignore */
    }
    try {
      await ctx.close();
    } catch {
      /* ignore */
    }
    await Promise.allSettled([mic.stop(), tab.stop()]);
  };

  return { stream, mode: "microphone-and-tab", stop };
}

function sessionFromSingleStream(
  stream: MediaStream,
  mode: CaptureMode,
): CaptureSession {
  let stopped = false;
  const stop = async (): Promise<void> => {
    if (stopped) return;
    stopped = true;
    for (const track of stream.getTracks()) {
      track.stop();
    }
  };
  return { stream, mode, stop };
}

export class WebAudioCaptureProvider implements AudioCaptureProvider {
  async start(request: CaptureRequest): Promise<CaptureSession> {
    switch (request.mode) {
      case "microphone":
        return startMicrophone(request);
      case "browser-tab":
        return startBrowserTab();
      case "microphone-and-tab":
        return startMicrophoneAndTab(request);
    }
  }

  isSupported(mode: CaptureMode): boolean {
    switch (mode) {
      case "microphone":
        return hasGetUserMedia();
      case "browser-tab":
        return hasGetDisplayMedia();
      case "microphone-and-tab":
        return (
          hasGetUserMedia() &&
          hasGetDisplayMedia() &&
          typeof AudioContext !== "undefined"
        );
    }
  }
}
