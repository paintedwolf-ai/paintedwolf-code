import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

const filesView = readFileSync(
  join(import.meta.dirname, "../components/ProjectFilesView.tsx"),
  "utf8",
);
const definitionNavigation = readFileSync(
  join(import.meta.dirname, "definition-navigation.ts"), "utf8",
);
const jumpBridge = readFileSync(
  join(import.meta.dirname, "../components/project-files-jump-bridge.ts"),
  "utf8",
);

describe("definition jump history push-before-jump", () => {
  it("pushes the origin before opening, then pushes the destination too", () => {
    const fn = definitionNavigation.slice(
      definitionNavigation.indexOf("const jumpToDefinitionCandidate"),
      definitionNavigation.indexOf("const runGoToDefinition"),
    );
    const originPushAt = fn.indexOf("pushProjectJump");
    const openAt = fn.indexOf("openFilesBuffer");
    const destinationPushAt = fn.indexOf("pushProjectJump", openAt);
    expect(originPushAt).toBeGreaterThan(-1);
    expect(openAt).toBeGreaterThan(originPushAt);
    // Back navigation retains the destination at the stack tip.
    expect(destinationPushAt).toBeGreaterThan(openAt);
  });

  it("keeps jump-back on the shared stack used by Ctrl+-", () => {
    expect(jumpBridge).toContain("export function pushProjectJump");
    expect(filesView).toContain('"files.jumpBack"');
    expect(filesView).toContain('"files.goToDefinition"');
  });

  it("keeps misses and lookup errors in the editor", () => {
    const fn = definitionNavigation.slice(
      definitionNavigation.indexOf("const runGoToDefinition"),
      definitionNavigation.indexOf("const observeKeyboardAndBuffer"),
    );
    expect(fn).toContain('outcome.kind === "miss"');
    expect(fn).toContain("flashDefinitionNotice");
    expect(fn).not.toContain("openDefinitionSearch");
    expect(fn).not.toContain("onOpenSearch");
  });
});
