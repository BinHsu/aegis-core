// frontend_web/src/main.tsx
//
// Entry point. React root + router. Per ADR-0002 Constraint 2, nothing
// here may call a browser API directly — all platform-specific paths
// (audio capture, transport, storage) go through provider interfaces in
// src/providers/ so a Phase 4+ Tauri wrap can swap implementations.
//
// Boot order (ADR-15): fetch runtime config FIRST, then initialize the
// gateway client and auth from it, THEN render. Config-dependent modules
// (lib/gateway-client, lib/auth) no longer do work at import time — they
// expose init*() functions called here, so the async config fetch
// completes before anything reads it.

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router-dom";

import { loadConfig } from "./lib/config";
import { initGatewayClient } from "./lib/gateway-client";
import { initAuth } from "./lib/auth";

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
      // OIDC redirect target for the Cloud-mode Cognito Hosted UI flow.
      // Local mode reaches this path too (handleSignInCallback is a
      // no-op) so the router config stays mode-agnostic.
      { path: "auth/callback", element: <AuthCallbackPage /> },
    ],
  },
]);

async function bootstrap(): Promise<void> {
  const cfg = await loadConfig();
  initGatewayClient(cfg);
  initAuth(cfg);

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
}

void bootstrap();
