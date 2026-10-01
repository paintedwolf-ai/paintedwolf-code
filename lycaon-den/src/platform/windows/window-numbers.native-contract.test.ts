import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { denRoot, loadSourceCorpus } from "../../test/source-corpus.ts";
import { MAIN_WINDOW_LABEL, MAIN_WINDOW_NUMBER } from "./window-numbers.ts";

const tauriRoot = join(denRoot, "src-tauri");

/** A `const NAME: u64 = <literal or OTHER + literal>;` declared in the native registry. */
function nativeNumber(source: string, name: string): number {
  const match = new RegExp(`const ${name}: u64 = ([A-Z_]+ \\+ )?(\\d+);`).exec(source);
  if (!match) expect.fail(`item_windows.rs no longer declares ${name}`);
  const base = match[1] ? nativeNumber(source, match[1].replace(" + ", "")) : 0;
  return base + Number(match[2]);
}

describe("window numbers mirror the native registry", () => {
  const registry = loadSourceCorpus(join(tauriRoot, "src"), { extensions: [".rs"] }).file("item_windows.rs");

  it("Window 1 is the main window on both sides, and peers start after it", () => {
    if (!registry) expect.fail("src-tauri/src/item_windows.rs is missing");
    expect(nativeNumber(registry.text, "MAIN_VIEW_NUMBER")).toBe(MAIN_WINDOW_NUMBER);
    expect(
      nativeNumber(registry.text, "FIRST_PEER_VIEW_NUMBER"),
      "Peer windows must never take the main window's number",
    ).toBeGreaterThan(MAIN_WINDOW_NUMBER);
    expect(registry.text).toContain("AtomicU64::new(FIRST_PEER_VIEW_NUMBER)");
  });

  it("the main window's native label is the one every platform config declares", () => {
    for (const file of ["tauri.conf.json", "tauri.macos.conf.json", "tauri.linux.conf.json"]) {
      const config = JSON.parse(readFileSync(join(tauriRoot, file), "utf8")) as {
        app?: { windows?: { label?: string }[] };
      };
      expect(config.app?.windows?.[0]?.label, file).toBe(MAIN_WINDOW_LABEL);
    }
  });
});
