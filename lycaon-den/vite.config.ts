import path from "node:path";
import { createHash } from "node:crypto";
import { readFileSync, readdirSync } from "node:fs";
import process from "node:process";
import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";
import solid from "vite-plugin-solid";
import { harnessRuntimePaths } from "./vite.runtime-paths.ts";
import { webviewCssTargets } from "./vite.webview-targets.ts";
import { transcriptLayoutPlugin } from "./vite.transcript-layout.ts";
import {
  defaultDenScrollDebugFile,
  denScrollDebugPlugin,
} from "./vite.den-scroll-debug.ts";

const denRoot = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(denRoot, "..");

const host = process.env.TAURI_DEV_HOST;

// The harness proxies API routes through the frontend origin.
const proxyTarget = process.env.VITE_LYCAON_PROXY_TARGET || "http://127.0.0.1:8787";
const proxyEnabled = process.env.VITE_LYCAON_PROXY === "1";
// Test runs freeze live reload so source edits cannot remount the page mid-step.
const frozen = process.env.LYCAON_E2E_FROZEN === "1";

const proxy = proxyEnabled
  ? {
      "/v1": { target: proxyTarget, changeOrigin: true },
      "/health": { target: proxyTarget, changeOrigin: true },
      "/harness": { target: proxyTarget, changeOrigin: true },
    }
  : undefined;

export default defineConfig(() => {
  const runtime = harnessRuntimePaths(process.env.LYCAON_E2E_STATE_DIR);
  if (!process.env.VITE_DEN_SCROLL_DEBUG_FILE) {
    process.env.VITE_DEN_SCROLL_DEBUG_FILE = runtime.scrollDebugFile ?? defaultDenScrollDebugFile();
  }

  // Patch contents invalidate optimized dependencies on startup.
  const patches = path.join(denRoot, "patches");
  const patchDigest = createHash("sha256");
  for (const name of readdirSync(patches).filter((name) => name.endsWith(".patch")).sort()) {
    patchDigest.update(name).update(readFileSync(path.join(patches, name)));
  }

  return {
    plugins: [{ name: `dependency-patches-${patchDigest.digest("hex")}` }, transcriptLayoutPlugin(denRoot), tailwindcss(), solid(), denScrollDebugPlugin()],

    envPrefix: ["VITE_", "TAURI_"],

    // Development serves the same prefixed and lowered CSS the webview receives in a build.
    css: {
      transformer: "lightningcss",
      lightningcss: { targets: webviewCssTargets(denRoot) },
    },

    build: {
      cssMinify: "lightningcss",
    },

    clearScreen: false,

    // Each harness keeps its own optimized modules.
    cacheDir: runtime.cacheDir,

    optimizeDeps: {
      include: ["marked", "isomorphic-dompurify"],
    },

    server: {
      port: 1420,
      strictPort: true,
      host: host || "127.0.0.1",
      // Development modules bypass the webview cache.
      headers: {
        "Cache-Control": "no-store",
      },
      proxy,
      // Raw changelog imports resolve from the repository root.
      fs: {
        allow: [denRoot, repoRoot],
      },
      hmr: frozen
        ? false
        : host
          ? {
              protocol: "ws",
              host,
              port: 1421,
            }
          : undefined,
      // Test-only edits leave the running interface mounted.
      watch: frozen
        ? null
        : {
            ignored: [
              "**/src-tauri/**",
              "**/*.test.ts",
              "**/*.test.tsx",
              "**/*.spec.ts",
              "**/e2e/**",
              "**/test-results/**",
              "**/playwright-report/**",
            ],
          },
    },
  };
});
