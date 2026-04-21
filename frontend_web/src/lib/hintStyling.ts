// frontend_web/src/lib/hintStyling.ts
//
// Pure mapping from `HintUrgency` to UI surface treatment. Lives here,
// not inline in the pages, so it can be unit-tested without a DOM /
// React harness — the frontend test stack is vitest + happy-dom only,
// no @testing-library/react is installed.
//
// Two surfaces returned per urgency:
//   - `style`:   the inline CSS object the page applies to the hint
//                container. Colors, borders, padding.
//   - `prominence`: semantic tier — "inline" (subtle, below the
//                transcript) or "banner" (pinned, above the transcript,
//                dismissable). Pages branch on this to decide where the
//                hint renders, not just how it looks.
//
// URGENT vs HIGH both return "banner" so the pinning behavior is
// symmetric — the visual color differs (amber vs red) so operators can
// still triage at a glance. LOW and NORMAL both return "inline" with
// different intensity; LOW is muted-gray (background RAG hit), NORMAL
// is blue (noticeable but not loud).

import type { HintUrgency } from "@/providers/TranscriptStreamProvider";
import { HintUrgency as ProtoHintUrgency } from "@/gen/proto/aegis/v1/aegis_pb";
import type { CSSProperties } from "react";

export type HintProminence = "inline" | "banner";

export interface HintStyleSpec {
  readonly prominence: HintProminence;
  readonly style: CSSProperties;
  /** Human-label for the urgency tier, shown in the banner/title.
   *  LOW/NORMAL do not show it — the style alone carries the tier. */
  readonly label: string | null;
}

/**
 * Convert the frontend's string-literal `HintUrgency` (see
 * `TranscriptStreamProvider/types.ts`) into the proto-generated enum
 * used on the wire when the host calls `SendOfficerHint`. Factored
 * here so the mapping is auditable in one place.
 */
export function toProtoUrgency(urgency: HintUrgency): ProtoHintUrgency {
  switch (urgency) {
    case "low":
      return ProtoHintUrgency.LOW;
    case "normal":
      return ProtoHintUrgency.NORMAL;
    case "high":
      return ProtoHintUrgency.HIGH;
    case "urgent":
      return ProtoHintUrgency.URGENT;
  }
}

export function hintStyleForUrgency(urgency: HintUrgency): HintStyleSpec {
  switch (urgency) {
    case "low":
      return {
        prominence: "inline",
        style: {
          padding: "0.5rem 0.75rem",
          background: "#f6f6f6",
          border: "1px solid #e0e0e0",
          borderRadius: "4px",
          color: "#555",
          fontSize: "0.9rem",
        },
        label: null,
      };
    case "normal":
      return {
        prominence: "inline",
        style: {
          padding: "0.75rem 1rem",
          background: "#e8f4fd",
          border: "1px solid #bce0f5",
          borderRadius: "4px",
          color: "#1f4e79",
        },
        label: null,
      };
    case "high":
      return {
        prominence: "banner",
        style: {
          padding: "0.75rem 1rem",
          background: "#fff3cd",
          border: "1px solid #ffc107",
          borderLeft: "4px solid #ffc107",
          borderRadius: "4px",
          color: "#5a4300",
          fontWeight: 500,
        },
        label: "HIGH",
      };
    case "urgent":
      return {
        prominence: "banner",
        style: {
          padding: "0.75rem 1rem",
          background: "#f8d7da",
          border: "1px solid #dc3545",
          borderLeft: "4px solid #dc3545",
          borderRadius: "4px",
          color: "#58151c",
          fontWeight: 600,
        },
        label: "URGENT",
      };
  }
}
