import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/api": { target: "http://127.0.0.1:8200", changeOrigin: true },
      "/health": { target: "http://127.0.0.1:8200", changeOrigin: true },
      // Static assets are served by knox-media, not Vite — without these, thumbnails 404 on :5173.
      "/uploads": { target: "http://127.0.0.1:8200", changeOrigin: true },
      "/metadata/library": { target: "http://127.0.0.1:8200", changeOrigin: true },
      "/static": { target: "http://127.0.0.1:8200", changeOrigin: true },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
