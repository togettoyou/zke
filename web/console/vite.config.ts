import { fileURLToPath, URL } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// ZKE Server keeps a strict same-origin model: session and CSRF cookies are
// same-site, and the Server applies Fetch Metadata cross-origin protection.
// The dev server therefore proxies API traffic instead of enabling CORS, and
// must not rewrite the Origin header (`changeOrigin: false`).
const serverTarget = process.env.ZKE_SERVER_URL ?? "http://127.0.0.1:8080";

const proxiedPrefixes = ["/api", "/healthz", "/readyz", "/agent-install"];

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    host: "127.0.0.1",
    port: 5173,
    proxy: Object.fromEntries(
      proxiedPrefixes.map((prefix) => [
        prefix,
        {
          target: serverTarget,
          changeOrigin: false,
          // Server-Sent Events must not be buffered by the dev proxy.
          ws: false,
        },
      ]),
    ),
  },
  build: {
    // Immutable asset names; index.html itself must not be cached long term.
    assetsDir: "assets",
    sourcemap: true,
  },
});
