import path from "path"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  build: {
    // Build into a subdir of the Go embed root (internal/web/dist), NOT the root
    // itself: the root holds a committed .gitkeep that makes `//go:embed all:dist`
    // resolve for a plain `go build` even when the SPA was never built. emptyOutDir
    // only ever wipes this app/ subdir, so a web build — success, failure, or
    // skipped — can never delete that placeholder and break the Go build.
    outDir:      "../internal/web/dist/app",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/v1": {
        target:       "http://127.0.0.1:8787",
        changeOrigin: true,
      },
    },
  },
})
