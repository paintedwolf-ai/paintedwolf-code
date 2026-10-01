import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");

describe("shell contract", () => {
  it("package.json must not list external SDK transport dependencies", () => {
    const pkg = JSON.parse(
      readFileSync(join(root, "package.json"), "utf8"),
    ) as { dependencies?: Record<string, string> };
    for (const dep of Object.keys(pkg.dependencies ?? {})) {
      expect(dep).not.toMatch(/@.*\/sdk$/);
    }
  });

  // An unguarded stage that throws keeps its last frame with nothing reported.
  it("every stage body mounts under StageErrorBoundary", () => {
    const shell = ["Shell.tsx", "ShellColumns.tsx"]
      .map((file) => readFileSync(join(root, "src/components/shell", file), "utf8"))
      .join("\n");
    const mounts = [...shell.matchAll(/<main class="den-shell-main"[^>]*>/g)];
    expect(mounts.length).toBe(2);
    for (const mount of mounts) {
      const body = shell.slice(
        mount.index! + mount[0].length,
        shell.indexOf("</main>", mount.index!),
      );
      expect(body).toContain("<StageErrorBoundary");
    }
  });
});
