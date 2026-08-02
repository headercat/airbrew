import path from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "tailwindcss";
import autoprefixer from "autoprefixer";

const apiProxyTarget =
  process.env.VITE_API_PROXY_TARGET ?? "http://127.0.0.1:5050";

// During development Vite serves on :5051 and proxies /api to the Go server.
// In production, `npm run build` emits static files into ../web/dist which the
// Go binary embeds via go:embed (see web/embed.go).
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  css: {
    postcss: {
      plugins: [tailwindcss(), autoprefixer()],
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    port: 5051,
    proxy: {
      "/api": apiProxyTarget,
      "/healthz": apiProxyTarget,
    },
  },
});
