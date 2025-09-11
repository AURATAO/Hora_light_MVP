import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// IMPORTANT: app will live at https://horaapp.co/app
export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: "/app/",
  server: { port: 5173 },
  build: { outDir: "dist" },
});
