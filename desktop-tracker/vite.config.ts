import { defineConfig } from "vite";

// Vite serves the tracker's tiny webview UI. Tauri points devUrl here.
export default defineConfig({
  clearScreen: false,
  server: { port: 5173, strictPort: true },
  build: { outDir: "dist", target: "es2021" },
});
