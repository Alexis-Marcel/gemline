import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

const BACKEND = "http://localhost:8080";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      // Keep the browser's Host: local accounts (AUTH_MODE=local) check that a
      // form or a cookie-authenticated call comes from the site itself.
      "/api": { target: BACKEND },
      // Local accounts' pages are served by the backend.
      "/compte": { target: BACKEND },
      "/ws": { target: BACKEND, changeOrigin: true, ws: true },
      "/healthz": { target: BACKEND },
      "/readyz": { target: BACKEND },
    },
  },
});
