// frontend_web/src/lib/transcriptExport.ts
//
// Pure formatting helpers for the transcript-export flow (ADR-0024
// Decision C, wired to the export button in Phase 3c Slice 5). No
// DOM / fetch / console — given a transcript + meeting metadata,
// produce a UTF-8 string the FileSystemProvider can hand to the OS
// save dialog. Speaker overrides (ARCH §9.2) are resolved here so
// the file reflects what the host saw on screen.

export interface TranscriptSegment {
  readonly id: string;
  readonly text: string;
  readonly isFinal: boolean;
  readonly speaker: string;
}

export interface TranscriptExportMeta {
  readonly sessionId: string;
  readonly userId: string;
  readonly title: string;
  readonly exportedAt: string; // ISO 8601 UTC
}

function resolveLabel(
  segment: TranscriptSegment,
  overrides: Readonly<Record<string, string>>,
): string {
  return overrides[segment.speaker] ?? segment.speaker;
}

/**
 * Filename suggestion — ISO date + sanitised title. Keeps the
 * suggestion OS-safe (no `/`, no control chars) so the Web impl's
 * anchor-download and the future Tauri `rfd::FileDialog` both
 * accept it without further munging.
 */
export function suggestedFilename(
  meta: TranscriptExportMeta,
  extension: "md" | "json",
): string {
  const datePart = meta.exportedAt.slice(0, 10); // "YYYY-MM-DD"
  const titlePart =
    meta.title.trim() === ""
      ? meta.sessionId
      : meta.title
          .trim()
          .toLowerCase()
          .replace(/[^a-z0-9]+/g, "-")
          .replace(/^-+|-+$/g, "")
          .slice(0, 48);
  return `aegis-transcript-${datePart}-${
    titlePart || meta.sessionId
  }.${extension}`;
}

/**
 * Markdown export. Final segments are rendered normally; interim
 * segments are marked with a trailing `(interim)` tag so downstream
 * readers can tell which lines were mid-stream approximations.
 */
export function formatTranscriptMarkdown(
  transcript: readonly TranscriptSegment[],
  overrides: Readonly<Record<string, string>>,
  meta: TranscriptExportMeta,
): string {
  const header = [
    `# Aegis meeting transcript`,
    ``,
    `- **Session**: \`${meta.sessionId}\``,
    `- **Title**: ${meta.title.trim() === "" ? "_(untitled)_" : meta.title}`,
    `- **Host**: \`${meta.userId}\``,
    `- **Exported**: ${meta.exportedAt}`,
    ``,
    `> Transcripts are the host's responsibility to store and share`,
    `> under applicable data-protection law (ADR-0024 Decision C).`,
    ``,
    `---`,
    ``,
  ].join("\n");
  const lines = transcript.map((seg) => {
    const label = resolveLabel(seg, overrides);
    const tail = seg.isFinal ? "" : " _(interim)_";
    return `- **${label}**: ${seg.text}${tail}`;
  });
  return header + lines.join("\n") + "\n";
}

/**
 * JSON export. Stable machine-readable shape — intentionally a flat
 * envelope so downstream tooling can parse without schema reflection.
 * Speaker overrides are materialised into `speakerDisplay` so a
 * consumer who doesn't know the override-map semantics still gets
 * the label the host meant.
 */
export function formatTranscriptJson(
  transcript: readonly TranscriptSegment[],
  overrides: Readonly<Record<string, string>>,
  meta: TranscriptExportMeta,
): string {
  const envelope = {
    kind: "aegis.transcript.export" as const,
    schemaVersion: 1 as const,
    meta,
    speakerOverrides: overrides,
    segments: transcript.map((seg) => ({
      id: seg.id,
      text: seg.text,
      isFinal: seg.isFinal,
      speakerDetected: seg.speaker,
      speakerDisplay: resolveLabel(seg, overrides),
    })),
  };
  return JSON.stringify(envelope, null, 2) + "\n";
}
