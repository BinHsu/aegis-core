// frontend_web/src/providers/AuthProvider/AegisAuthShell.test.ts
//
// Coverage for the pure `userToPrincipal` claim-mapper extracted from
// AegisAuthShell. The hook + AuthContextProvider behavior itself
// requires @testing-library/react (not currently installed); a full
// component-render harness is a separate scope decision.

import { describe, expect, test } from "vitest";
import type { User } from "oidc-client-ts";

import { userToPrincipal } from "./AegisAuthShell";

// Build a minimal User fixture. oidc-client-ts's User is a large
// class; for the mapper we only need `profile` to be shaped right.
// Cast through unknown to suppress the structural-mismatch warning.
function fakeUser(profile: Record<string, unknown>): User {
  return { profile } as unknown as User;
}

describe("userToPrincipal", () => {
  test("maps sub + custom:tenant_id to Principal shape", () => {
    const u = fakeUser({
      sub: "cognito-user-abc",
      "custom:tenant_id": "tenant-42",
    });
    const p = userToPrincipal(u);
    expect(p.userId).toBe("cognito-user-abc");
    expect(p.tenantId).toBe("tenant-42");
    expect(p.mode).toBe("cloud");
    expect(p.displayName).toBeUndefined();
    expect(p.email).toBeUndefined();
  });

  test("treats missing custom:tenant_id as empty string", () => {
    const u = fakeUser({ sub: "cognito-user-abc" });
    const p = userToPrincipal(u);
    expect(p.tenantId).toBe("");
  });

  test("treats non-string custom:tenant_id as empty string", () => {
    const u = fakeUser({
      sub: "cognito-user-abc",
      "custom:tenant_id": 42, // pathological IdP — should not crash
    });
    const p = userToPrincipal(u);
    expect(p.tenantId).toBe("");
  });

  test("populates displayName when profile.name present", () => {
    const u = fakeUser({
      sub: "cognito-user-abc",
      "custom:tenant_id": "t1",
      name: "Ada Lovelace",
    });
    const p = userToPrincipal(u);
    expect(p.displayName).toBe("Ada Lovelace");
  });

  test("populates email when profile.email present (and name absent)", () => {
    const u = fakeUser({
      sub: "cognito-user-abc",
      "custom:tenant_id": "t1",
      email: "ada@example.com",
    });
    const p = userToPrincipal(u);
    expect(p.email).toBe("ada@example.com");
    expect(p.displayName).toBeUndefined();
  });

  test("prefers displayName over email when both present", () => {
    // Intentional priority — the existing user-facing "Signed in as
    // <displayName>" rendering looks better with the human name; the
    // email is the fallback when Cognito hasn't collected a name.
    const u = fakeUser({
      sub: "cognito-user-abc",
      "custom:tenant_id": "t1",
      name: "Ada Lovelace",
      email: "ada@example.com",
    });
    const p = userToPrincipal(u);
    expect(p.displayName).toBe("Ada Lovelace");
    expect(p.email).toBeUndefined();
  });

  test("rejects empty string name / email (no cosmetic empty field)", () => {
    const u = fakeUser({
      sub: "cognito-user-abc",
      "custom:tenant_id": "t1",
      name: "",
      email: "",
    });
    const p = userToPrincipal(u);
    // Neither appended — both guarded against empty-string.
    expect(p.displayName).toBeUndefined();
    expect(p.email).toBeUndefined();
  });
});
