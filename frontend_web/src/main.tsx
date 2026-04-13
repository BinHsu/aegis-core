// frontend_web/src/main.tsx
//
// Entry point. React root + router. Per ADR-0002 Constraint 2, nothing
// here may call a browser API directly — all platform-specific paths
// (audio capture, transport, storage) go through provider interfaces
// in src/providers/ so Phase 4+ Tauri wrap can swap implementations.

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router-dom";

import { App } from "./App";
import { HostPage } from "./pages/Host/HostPage";
import { ViewerPage } from "./pages/Viewer/ViewerPage";

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
    ],
  },
]);

const rootElement = document.getElementById("root");
if (!rootElement) {
  throw new Error("frontend_web: #root element missing from index.html");
}

createRoot(rootElement).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
);
