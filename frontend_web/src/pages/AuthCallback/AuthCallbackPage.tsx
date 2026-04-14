// frontend_web/src/pages/AuthCallback/AuthCallbackPage.tsx
//
// Mounted at `/auth/callback` — the Cognito Hosted UI redirects here
// after a successful login carrying `?code=…&state=…`. Calls into the
// AuthProvider singleton's `handleSignInCallback`, which exchanges
// the code for tokens, then bounces the user to /host.
//
// In Local mode this page is reachable but does nothing useful; the
// LocalAuthProvider's handleSignInCallback is a no-op, then we still
// navigate to /host. Wiring it through the same code path keeps the
// router config mode-agnostic.

import { useEffect, useState, type JSX } from "react";
import { useNavigate } from "react-router-dom";

import { auth } from "@/lib/auth";

type CallbackState =
  | { kind: "processing" }
  | { kind: "done" }
  | { kind: "error"; message: string };

export function AuthCallbackPage(): JSX.Element {
  const navigate = useNavigate();
  const [state, setState] = useState<CallbackState>({ kind: "processing" });

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        await auth.handleSignInCallback();
        if (cancelled) return;
        setState({ kind: "done" });
        // Replace history so the back button doesn't re-trigger the
        // callback handler (whose code+state are now consumed and
        // would fail second time).
        navigate("/host", { replace: true });
      } catch (err) {
        if (cancelled) return;
        setState({
          kind: "error",
          message: err instanceof Error ? err.message : String(err),
        });
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [navigate]);

  return (
    <main>
      <h2>Signing you in…</h2>
      {state.kind === "processing" && (
        <p style={{ color: "#666" }}>
          Exchanging authorization code with Cognito…
        </p>
      )}
      {state.kind === "done" && (
        <p style={{ color: "#666" }}>Done. Redirecting to host page…</p>
      )}
      {state.kind === "error" && (
        <>
          <p style={{ color: "#c0392b" }}>
            <strong>Sign-in failed:</strong> {state.message}
          </p>
          <p>
            <a href="/">Back to landing</a>
          </p>
        </>
      )}
    </main>
  );
}
