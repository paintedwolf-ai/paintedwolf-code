import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { describe, expect, it } from "vitest";

const denRoot = join(import.meta.dirname, "..", "..", "..", "..");
const patchPath = "patches/@replit+codemirror-emacs@6.1.0-registration-side-effects.patch";

describe("CodeMirror Emacs patch", () => {
  it("keeps key and command registration through bundler tree shaking", () => {
    const packageJson = JSON.parse(
      readFileSync(join(denRoot, "package.json"), "utf8"),
    ) as { patchedDependencies?: Record<string, string> };
    expect(packageJson.patchedDependencies?.["@replit/codemirror-emacs@6.1.0"]).toBe(patchPath);

    // A pure annotation lets the dependency optimizer and production build drop every command.
    const entry = createRequire(join(denRoot, "package.json")).resolve("@replit/codemirror-emacs");
    const source = readFileSync(join(dirname(entry), "index.js"), "utf8");
    expect(source).toMatch(/^EmacsHandler\.addCommands\(\{$/m);
    expect(source).not.toMatch(/__PURE__\*\/EmacsHandler\.(addCommands|bindKey)\(/);
  });
});
