// @vitest-environment jsdom
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { openAPISchemaProperties, readOpenAPIBundle } from "../api/openapi-sync.ts";
import { repoRootDir } from "./connection/host-authority-registry.ts";

/** Recovery fields come from the host. */
const repoRoot = repoRootDir();
const denSrc = join(repoRoot, "lycaon-den/src");

/** Searches source without matching this file's pattern literals. */
function rg(pattern: string, ...paths: string[]): string {
  try {
    return execFileSync(
      "rg",
      ["-n", "-g", "!chat-usability-invariants.test.ts", pattern, ...paths],
      { encoding: "utf8", cwd: repoRoot },
    ).trim();
  } catch (err: unknown) {
    if ((err as { status?: number }).status === 1) return "";
    throw err;
  }
}

function read(rel: string): string {
  return readFileSync(join(repoRoot, rel), "utf8");
}

describe("recovery is host-decided", () => {
  it("the wire fields Den branches on exist in the bundled spec", () => {
    const yaml = readOpenAPIBundle();
    const sessionEvent = openAPISchemaProperties(yaml, "SessionEvent");
    expect(sessionEvent).toContain("idle_disposition");
    const rewind = openAPISchemaProperties(yaml, "RewindSessionResponse");
    expect(rewind).toContain("restored_prompt");
  });

  it("stopped-vs-interrupted-vs-error branches on idle_disposition alone", () => {
    const src = read("lycaon-den/src/components/transcript/TurnOutcomeMarker.tsx");
    expect(src).toContain("user_stopped");
    expect(src).toContain("turn_error");
    // Host teardown stays distinct from failure.
    expect(src).toContain("interrupted");
    // One typed field drives all outcomes.
    expect(src).not.toMatch(/\.message\b|err\.|catch\s*\(/);
  });

  it("rewind eligibility is role and visibility, not prose", () => {
    const src = read("lycaon-den/src/chat/recovery/session-recovery.ts");
    expect(src).toContain('role === "user"');
    expect(src).toContain('visibility !== "internal"');
  });
});

describe("drafts and recall stay presentation-only", () => {
  it("neither reaches a prompt, message, or transcript payload builder", () => {
    // Draft and recall stores contain unsent text only.
    const hits = rg(
      "from .*(composer-drafts|prompt-recall)",
      join(denSrc, "api"),
      join(denSrc, "chat/session"),
      join(denSrc, "chat/transcript"),
    );
    expect(hits, `unsent-text stores reached a wire path:\n${hits}`).toBe("");
  });

  it("the recall ring is not persisted anywhere", () => {
    const src = read("lycaon-den/src/chat/composer/prompt-recall.ts");
    expect(src).not.toContain("persistAppState");
    expect(src).not.toContain("localStorage");
  });
});

describe("recovery introduces no heuristics", () => {
  it("no prose matching on the recovery paths", () => {
    const forbidden = [
      "toLowerCase\\(\\)\\.includes",
      "\\.startsWith\\(\"(stopped|error|undo)",
      "/(stopped|failed|error)/i",
    ].join("|");
    const hits = rg(forbidden, join(denSrc, "chat/recovery"));
    expect(hits, `prose matching on a recovery path:\n${hits}`).toBe("");
  });

  it("the row toolbar stays row-local so windowing cannot break it", () => {
    const src = read("lycaon-den/src/components/transcript/MessageActions.tsx");
    // Virtualized rows keep actions within the mounted row.
    expect(src).not.toContain("document.querySelector");
    expect(src).not.toContain("getElementById");
  });

  it("no second undo surface beside the one rewind route", () => {
    const hits = rg("unrevert|softRevert|undoPromote|restoreCheckpoint", denSrc);
    expect(hits, `a parallel undo mechanism appeared:\n${hits}`).toBe("");
  });

  it("the UI never calls session recovery a checkpoint", () => {
    // Keep recovery and approval terminology distinct.
    const src =
      read("lycaon-den/src/components/transcript/SessionRecoverDialog.tsx") +
      read("lycaon-den/src/components/transcript/MessageActions.tsx");
    expect(src.toLowerCase()).not.toContain("checkpoint");
  });

  it("recovery keys go through the shortcut registry, not a local keydown", () => {
    // Keep recovery keys in the shared shortcut registry.
    const src = read("lycaon-den/src/components/chatview/Composer.tsx");
    expect(src).toMatch(/registerCommandHandler\(\s*"composer\.recallPrev"/);
    expect(src).toMatch(/registerCommandHandler\(\s*"composer\.recallNext"/);
    expect(src).not.toMatch(/onKeyDown=\{[^}]*[Rr]ecall/);
  });

  it("the confirm dialog uses the shared focus trap, not a parallel Escape", () => {
    // The shared trap handles Escape before focus reaches the dialog.
    const src = read("lycaon-den/src/components/transcript/SessionRecoverDialog.tsx");
    expect(src).toContain("createModalFocusTrap");
    expect(src).not.toMatch(/onKeyDown/);
  });

  it("edit does not mutate the message row in place", () => {
    const hits = rg("contentEditable|patchMessage\\(|updateMessageContent\\(", denSrc);
    expect(hits, `an in-place bubble editor appeared:\n${hits}`).toBe("");
  });
});
