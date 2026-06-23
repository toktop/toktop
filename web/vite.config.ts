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
    outDir:      "../internal/web/dist",
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
