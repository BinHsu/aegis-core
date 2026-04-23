// frontend_web/src/main.tsx
//
// Entry point. React root + router. Per ADR-0002 Constraint 2, nothing
// here may call a browser API directly — all platform-specific paths
// (audio capture, transport, storage) go through provider interfaces
// in src/providers/ so Phase 4+ Tauri wrap can swap implementations.

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router-dom";

// Load the auth singleton early — its module-side-effect constructs
// the Cognito UserManager (Cloud mode) and wires the gateway-client
// token interceptor before any RPC runs.
import "./lib/auth";

import { App } from "./App";
import { AuthCallbackPage } from "./pages/AuthCallback/AuthCallbackPage";
import { HostPage } from "./pages/Host/HostPage";
import { ViewerPage } from "./pages/Viewer/ViewerPage";
import { AegisAuthShell } from "./providers/AuthProvider";

const router = createBrowserRouter([
  {
    path: "/",
    element: <App />,
    children: [
      // Host role — staff operating the meeting.
      { path: "host", element: <HostPage /> },
      // Viewer role — boss / observers joining via invite link:
      //   /view/<session_id>?token=<jwt>
      // per ADR-0001.
      { path: "view/:sessionId", element: <ViewerPage /> },
      // OIDC redirect target for the Cloud-mode Cognito Hosted UI
      // flow. Local mode reaches this path too (LocalAuthProvider's
      // handleSignInCallback is a no-op) so the router config stays
      // mode-agnostic.
      { path: "auth/callback", element: <AuthCallbackPage /> },
    ],
  },
]);

const rootElement = document.getElementById("root");
if (!rootElement) {
  throw new Error("frontend_web: #root element missing from index.html");
}

createRoot(rootElement).render(
  <StrictMode>
    <AegisAuthShell>
      <RouterProvider router={router} />
    </AegisAuthShell>
  </StrictMode>,
);
