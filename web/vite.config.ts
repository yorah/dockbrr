/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react-swc";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath, URL } from "node:url";

// The API listens on 3625 in dev (see cmd/dockbrr defaults). The SPA build
// outputs into internal/httpapi/dist so spa.go can embed it (go:embed cannot
// reach a sibling web/dist).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": { target: "http://localhost:3625", changeOrigin: false },
      "/healthz": { target: "http://localhost:3625", changeOrigin: false },
    },
  },
  build: {
    outDir: "../internal/httpapi/dist",
    emptyOutDir: true,
  },
  test: {
    globals: true,
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    css: false,
  },
});
