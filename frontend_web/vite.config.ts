// vite.config.ts — Aegis Core frontend build config.
//
// Minimal Vite setup. Static assets go to dist/, served by CloudFront +
// S3 in Cloud mode (per ARCHITECTURE.md §1) or by the Go Gateway itself
// in Local mode. No special proxying here — the client talks to the
// gateway at a runtime-configured URL (env-var at build time for now;
// ADR-0002 Constraint 2 provider abstractions handle transport swap).

import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
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
  },
});
