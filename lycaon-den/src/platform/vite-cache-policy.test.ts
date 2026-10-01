import { readFileSync } from "node:fs";
import { dirname, isAbsolute, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it, vi } from "vitest";
import { harnessRuntimePaths } from "../../vite.runtime-paths.ts";

describe("Vite development cache policy", () => {
  it("prevents WKWebView from retaining HMR module variants", () => {
    const denRoot = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
    const config = readFileSync(join(denRoot, "vite.config.ts"), "utf8");

    expect(config).toContain('"Cache-Control": "no-store"');
  });

  it("keeps every mutable frontend path inside its owning harness", () => {
    const roots = ["/tmp/first harness", "/tmp/second café", "relative-state"];
    const allPaths = new Set<string>();
    for (const root of roots) {
      const paths = harnessRuntimePaths(root);
      expect(paths.cacheDir).toBeDefined();
      expect(paths.scrollDebugFile).toBeDefined();
      for (const value of Object.values(paths)) {
        expect(isAbsolute(value)).toBe(true);
        const fromRoot = relative(resolve(root), value);
        expect(isAbsolute(fromRoot)).toBe(false);
        expect(fromRoot.startsWith("..")).toBe(false);
        expect(allPaths.has(value)).toBe(false);
        allPaths.add(value);
      }
      expect(harnessRuntimePaths(root)).toEqual(paths);
    }
  });

  it.each([undefined, "", "   "])("retains ordinary development defaults without a harness: %s", (root) => {
    expect(harnessRuntimePaths(root)).toEqual({});
  });

  it("resolves independent Vite caches for concurrent harnesses in one checkout", async () => {
    const denRoot = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
    const { resolveConfig } = await import("vite");
    const caches = new Set<string>();
    try {
      for (const state of ["/tmp/harness one", "/tmp/harness café two"]) {
        vi.stubEnv("LYCAON_E2E_STATE_DIR", state);
        vi.stubEnv("VITE_DEN_SCROLL_DEBUG_FILE", "");
        const config = await resolveConfig({ root: denRoot, configFile: join(denRoot, "vite.config.ts") }, "serve");
        expect(config.cacheDir).toBe(join(state, "runtime", "vite-cache"));
        expect(process.env.VITE_DEN_SCROLL_DEBUG_FILE).toBe(join(state, "config", "debug", "den-scroll.jsonl"));
        caches.add(config.cacheDir);
      }
      expect(caches.size).toBe(2);
    } finally {
      vi.unstubAllEnvs();
    }
  });
});
