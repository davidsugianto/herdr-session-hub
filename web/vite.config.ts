import { defineConfig, type Plugin } from "vite";
import preact from "@preact/preset-vite";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";

// Emits sw.js: precache of the hashed shell, network-first navigation, never /api.
function swPlugin(): Plugin {
  return {
    name: "hub-sw",
    apply: "build",
    generateBundle(_o, bundle) {
      const files = Object.keys(bundle).map((f) => "/" + f);
      const list = ["/", "/manifest.webmanifest", "/icon-192.png", ...files].sort();
      const ver = createHash("sha256").update(list.join("|")).digest("hex").slice(0, 10);
      const tpl = readFileSync(new URL("./src/sw.template.js", import.meta.url), "utf8");
      this.emitFile({
        type: "asset",
        fileName: "sw.js",
        source: tpl.replace("__VERSION__", ver).replace("__PRECACHE__", JSON.stringify(list)),
      });
    },
  };
}

export default defineConfig({
  plugins: [preact(), swPlugin()],
  build: { outDir: "dist", emptyOutDir: true, assetsInlineLimit: 0, cssCodeSplit: false, target: "es2022" },
  server: { proxy: { "/api": "http://127.0.0.1:7878" } },
});
