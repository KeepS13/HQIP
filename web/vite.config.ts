import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api/v1/user": "http://127.0.0.1:8081",
      "/api/v2/client": "http://127.0.0.1:8081",
      "/usub": "http://127.0.0.1:8081",
      "/api": "http://127.0.0.1:8080",
      "/sub": "http://127.0.0.1:8081",
    },
  },
  build: { sourcemap: false },
});
