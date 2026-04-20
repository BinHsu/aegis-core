// vite.config.ts — Aegis Core frontend build config.
//
// Minimal Vite setup. Static assets go to dist/, served by CloudFront +
// S3 in Cloud mode (per ARCHITECTURE.md §1) or by the Go Gateway itself
// in Local mode. No special proxying here — the client talks to the
// gateway at a runtime-configured URL (env-var at build time for now;
// ADR-0002 Constraint 2 provider abstractions handle transport swap).
//
// Vitest section below shares this config so unit tests resolve the
// same `@/` path alias and plugin stack as `vite dev` / `vite build`.
// Without the shared config, a `from "@/gen/…"` import in a test file
// would fail with "cannot resolve" — typecheck would still pass (tsc
// reads tsconfig paths) but the test runner wouldn't.

/// <reference types="vitest" />
import { fileURLToPath, URL } from "node:url";

import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  resolve: {
    // Mirror tsconfig.json `paths` for Rollup. TypeScript resolves
    // `@/…` at typecheck via the paths map, but Vite/Rollup build
    // time uses a separate resolver that does NOT read tsconfig —
    // both have to agree or we get "Rollup failed to resolve import"
    // at bundle time while tsc is happy.
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  build: {
    target: "es2022",
    sourcemap: true,
    // Keep chunking predictable so Cloud-mode cache invalidation by
    // filename works cleanly.
    rollupOptions: {
      output: {
        manualChunks: {
          react: ["react", "react-dom", "react-router-dom"],
        },
      },
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    // Bind on all interfaces (0.0.0.0) so devices on the same LAN
    // can reach the dev server — required for the "host-scans-QR,
    // boss-scans-QR-on-phone" flow per ADR-0007. Vite prints the
    // first non-loopback IPv4 under "Network:" when this is on.
    host: true,
  },
  test: {
    // happy-dom is preferred over jsdom for Vitest: faster startup,
    // smaller, and our unit tests don't need jsdom-only APIs (we touch
    // `WebSocket`, `ArrayBuffer`, and plain DOM constants only).
    environment: "happy-dom",
    globals: false, // force explicit `import { test, expect } from "vitest"`
    // Match Vitest defaults but scope to src/ so Playwright's e2e/
    // folder (*.spec.ts) is not swept in here.
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
  },
});
