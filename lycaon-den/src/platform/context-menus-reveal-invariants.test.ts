/** Context menus + reveal architecture guards (docs/den-context-menus.md C2/C8). */
import { execSync } from "node:child_process";
import { existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const denSrc = join(dirname(fileURLToPath(import.meta.url)), "..");
// Components live under components/ and the Files feature folder.
const componentDirs = [join(denSrc, "components"), join(denSrc, "files")];
const platformDir = join(denSrc, "platform");
const tauriReveal = join(
  denSrc,
  "..",
  "src-tauri",
  "src",
  "reveal_in_file_manager.rs",
);

function rgHits(pattern: string, cwd: string): string[] {
  const out = execSync(
    `rg -n '${pattern}' "${cwd}" -g '*.ts' -g '*.tsx' -g '!**/node_modules/**' -g '!**/*.test.ts' -g '!**/*.test.tsx' -g '!**/context-menus-reveal-invariants*' 2>/dev/null || true`,
    { encoding: "utf8" },
  ).trim();
  if (!out) return [];
  return out.split("\n").filter((line) => line.length > 0);
}

function componentHits(pattern: string): string[] {
  return componentDirs.flatMap((dir) => rgHits(pattern, dir));
}

describe("context menus + reveal invariants", () => {
  it("forbids component-level OS reveal spawn (open -R / explorer / xdg-open)", () => {
    // OS reveal commands belong to the platform module.
    expect(componentHits("open -R")).toEqual([]);
    expect(componentHits("/select,")).toEqual([]);
    expect(componentHits("xdg-open")).toEqual([]);
    expect(componentHits("FileManager1")).toEqual([]);
  });

  it("keeps reveal_in_file_manager invoke only in the platform module", () => {
    const hits = rgHits("reveal_in_file_manager", denSrc);
    const nonPlatform = hits.filter(
      (line) => !line.includes("platform/files/reveal-in-file-manager.ts"),
    );
    expect(nonPlatform).toEqual([]);
    expect(hits.some((line) => line.includes("platform/files/reveal-in-file-manager.ts"))).toBe(
      true,
    );
  });

  it("ships exactly one ContextMenu component file", () => {
    expect(existsSync(join(denSrc, "components", "ContextMenu.tsx"))).toBe(true);
    expect(componentHits("export function ContextMenu")).toEqual([
      expect.stringContaining("ContextMenu.tsx"),
    ]);
    // Length 1 — only the shared chrome exports ContextMenu.
    expect(componentHits("export function ContextMenu")).toHaveLength(1);
  });

  it("keeps reveal-in-file-manager platform + Tauri modules present", () => {
    expect(existsSync(join(platformDir, "files", "reveal-in-file-manager.ts"))).toBe(true);
    expect(existsSync(tauriReveal)).toBe(true);
  });
});
