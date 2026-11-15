import path from "node:path"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"

const knowledgeProxy = {
  target: process.env.KNOWLEDGE_URL ?? "http://knowledge:9988",
  changeOrigin: true,
  rewrite: (requestPath: string) => requestPath.replace(/^\/api\/knowledge/, ""),
}

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "./src"),
    },
  },
  server: {
    host: "0.0.0.0",
    port: 5173,
    strictPort: true,
    watch: {
      usePolling: true,
      interval: 300,
    },
    proxy: {
      "/api/knowledge/v1": knowledgeProxy,
      "/api/knowledge/ping": knowledgeProxy,
      "/api": {
        target: process.env.RUNWAY_URL ?? "http://127.0.0.1:10400",
        changeOrigin: true,
        rewrite: (requestPath) => requestPath.replace(/^\/api/, ""),
      },
    },
  },
})
