// frontend_web/src/lib/hintStyling.test.ts
//
// Pure-function coverage for the urgency → render-spec mapping used
// by both HostPage and ViewerPage. Tested at this layer (not via
// rendered components) because the frontend stack is vitest +
// happy-dom only; @testing-library/react is not installed, so a pure-
// function contract is the correct grain.

import { describe, expect, test } from "vitest";

import { HintUrgency as ProtoHintUrgency } from "@/gen/proto/aegis/v1/aegis_pb";

import { hintStyleForUrgency, toProtoUrgency } from "./hintStyling";

describe("hintStyleForUrgency", () => {
  test("LOW and NORMAL render inline (no banner treatment)", () => {
    expect(hintStyleForUrgency("low").prominence).toBe("inline");
    expect(hintStyleForUrgency("normal").prominence).toBe("inline");
  });

  test("HIGH and URGENT render as a pinned banner", () => {
    expect(hintStyleForUrgency("high").prominence).toBe("banner");
    expect(hintStyleForUrgency("urgent").prominence).toBe("banner");
  });

  test("LOW and NORMAL have no urgency label (visual alone carries tier)", () => {
    expect(hintStyleForUrgency("low").label).toBeNull();
    expect(hintStyleForUrgency("normal").label).toBeNull();
  });

  test("HIGH and URGENT surface a human label for operator triage", () => {
    expect(hintStyleForUrgency("high").label).toBe("HIGH");
    expect(hintStyleForUrgency("urgent").label).toBe("URGENT");
  });

  test("every tier returns a style object with padding + border", () => {
    for (const u of ["low", "normal", "high", "urgent"] as const) {
      const spec = hintStyleForUrgency(u);
      expect(spec.style.padding).toBeDefined();
      expect(spec.style.border).toBeDefined();
      expect(spec.style.borderRadius).toBeDefined();
    }
  });

  test("HIGH and URGENT apply a thicker left-border for visual priority", () => {
    expect(hintStyleForUrgency("high").style.borderLeft).toContain("4px");
    expect(hintStyleForUrgency("urgent").style.borderLeft).toContain("4px");
  });
});

describe("toProtoUrgency", () => {
  test("maps each frontend urgency tier to the matching proto enum", () => {
    expect(toProtoUrgency("low")).toBe(ProtoHintUrgency.LOW);
    expect(toProtoUrgency("normal")).toBe(ProtoHintUrgency.NORMAL);
    expect(toProtoUrgency("high")).toBe(ProtoHintUrgency.HIGH);
    expect(toProtoUrgency("urgent")).toBe(ProtoHintUrgency.URGENT);
  });

  test("never returns UNSPECIFIED — the gateway rejects that urgency and staff must pick a tier", () => {
    for (const u of ["low", "normal", "high", "urgent"] as const) {
      expect(toProtoUrgency(u)).not.toBe(ProtoHintUrgency.UNSPECIFIED);
    }
  });
});
