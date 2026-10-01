import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { transform } from "lightningcss";
import { describe, expect, it } from "vitest";
import { webviewCssTargets } from "../../vite.webview-targets.ts";

const denRoot = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

function bundleWithMinimum(minimum: string): string {
  const root = mkdtempSync(join(tmpdir(), "den-webview-targets-"));
  mkdirSync(join(root, "src-tauri"));
  writeFileSync(
    join(root, "src-tauri", "tauri.conf.json"),
    JSON.stringify({ bundle: { macOS: { minimumSystemVersion: minimum } } }),
  );
  return root;
}

describe("webview CSS targets", () => {
  it("targets the WebKit of the bundle's oldest supported macOS", () => {
    expect(webviewCssTargets(denRoot).safari >> 16).toBe(17);
    expect(webviewCssTargets(bundleWithMinimum("15.2")).safari >> 16).toBe(18);
  });

  it("refuses a macOS minimum without a known WebKit", () => {
    expect(() => webviewCssTargets(bundleWithMinimum("12.0"))).toThrow(/macOS minimum 12.0/);
  });

  it.each(["serve", "build"] as const)("prefixes stylesheets through the project's own %s pipeline", async (command) => {
    const { preprocessCSS, resolveConfig } = await import("vite");
    const config = await resolveConfig(
      { root: denRoot, configFile: join(denRoot, "vite.config.ts"), logLevel: "silent" },
      command,
    );
    const { code } = await preprocessCSS(".summary { user-select: none; }", join(denRoot, "src", "probe.css"), config);
    expect(code).toContain("-webkit-user-select");
  }, 60_000);

  it("prefixes properties the webview supports only with a vendor prefix", () => {
    const { code } = transform({
      filename: "probe.css",
      code: new TextEncoder().encode(".summary { user-select: none; }"),
      targets: webviewCssTargets(denRoot),
    });
    expect(new TextDecoder().decode(code)).toContain("-webkit-user-select: none");
  });
});
