import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  server: {
    port: 5173,
    proxy: {
      "/asterism.v1.": {
        target: "http://127.0.0.1:7700",
        changeOrigin: false,
      },
      "/api/": {
        target: "http://127.0.0.1:7700",
        changeOrigin: false,
      },
      "/mcp": {
        target: "http://127.0.0.1:7700",
        changeOrigin: false,
      },
    },
  },
  build: {
    target: "es2022",
    rollupOptions: {
      output: {
        // Rollup moves a manual chunk's unassigned dependencies into it, so
        // React and the CJS helpers get their own chunk; otherwise they land
        // in `xyflow` and the entry preloads the whole graph library.
        manualChunks(id) {
          if (
            /\/node_modules\/(react|react-dom|scheduler|use-sync-external-store)\//.test(id) ||
            id.includes("commonjsHelpers")
          ) {
            return "react";
          }
          if (id.includes("/node_modules/@xyflow/")) return "xyflow";
          if (id.includes("/node_modules/uplot/") || id.includes("/node_modules/recharts/")) return "charts";
          return undefined;
        },
      },
    },
  },
});
