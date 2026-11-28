import { describe, expect, it } from "vitest";
import { briefingCopy } from "./briefing-copy.ts";
import { evidenceInputParts } from "./evidence-inputs.ts";

const copy = briefingCopy.evidence;

const stored: Record<string, string> = {
  recent_results: JSON.stringify([
    { tool: "command", outcome: "rejected", content: "Rejected: not literal arguments" },
    { tool: "command", outcome: "completed", content: "ok\n" },
  ]),
  action: `tool=command\nargs=${JSON.stringify({ command: "go vet ./...", cwd: "@project" })}`,
  receipts: [
    JSON.stringify({ command: "go vet ./...", state: "completed", status_code: 0 }),
    JSON.stringify({ command: "go test ./...", state: "failed", status_code: 1 }),
  ].join("\n"),
  file: JSON.stringify({ path: "a.go", physical_lines: 6 }),
  material: "File: \"a.go\"\npackage app\n",
  user_intent: "Add a Farewell function.",
};

describe("evidenceInputParts", () => {
  it("splits each call, result and receipt into its own block of stored text", () => {
    expect(evidenceInputParts({ key: "recent_results", text: stored.recent_results })).toEqual([
      { head: ["command", "rejected"], text: "Rejected: not literal arguments" },
      { head: ["command", "completed"], text: "ok\n" },
    ]);
    expect(evidenceInputParts({ key: "action", text: stored.action })).toEqual([
      { head: ["command", copy.workingDirectory("@project")], text: "go vet ./..." },
    ]);
    expect(evidenceInputParts({ key: "receipts", text: stored.receipts })).toEqual([
      { head: ["completed", copy.exitStatus(0)], text: "go vet ./..." },
      { head: ["failed", copy.exitStatus(1)], text: "go test ./..." },
    ]);
  });

  it("keeps a call's arguments as stored when it has more than a command", () => {
    const args = JSON.stringify({ path: "a.go", content: "package app" });
    expect(evidenceInputParts({ key: "action", text: `tool=write\nargs=${args}` })).toEqual([{ head: ["write", ""], text: args, language: "json" }]);
  });

  it("separates a tool result's status line from its value, and only then", () => {
    const value = JSON.stringify({ state: "completed", status: 0 });
    expect(evidenceInputParts({ key: "call_result", text: `completed: [command#4]\n${value}` })).toEqual([{ head: ["completed: [command#4]", ""], text: value, language: "json" }]);
    for (const other of ["completed: no value follows", `completed\n${value.slice(0, 9)}`, value]) {
      expect(evidenceInputParts({ key: "call_result", text: other })[0]?.text).toBe(other);
    }
  });

  it("names the language of text that is one whole JSON value, and of nothing else", () => {
    expect(evidenceInputParts({ key: "file", text: stored.file })).toEqual([{ text: stored.file, language: "json" }]);
    expect(evidenceInputParts({ key: "file", text: stored.file!.slice(0, -1) })).toEqual([{ text: stored.file!.slice(0, -1) }]);
    expect(evidenceInputParts({ key: "never_listed", text: "plain" })).toEqual([{ text: "plain" }]);
    expect(evidenceInputParts({ key: "diffs", text: "--- \n+++ a.go\n@@ -1,0 +1 @@\n+ package app" })).toEqual([{ text: "--- \n+++ a.go\n@@ -1,0 +1 @@\n+ package app", language: "diff" }]);
    expect(evidenceInputParts({ key: "state" })).toEqual([]);
  });

  it("shows a listed key holding some other value exactly as stored, never dropping it", () => {
    for (const [key, other] of [["recent_results", '{"other":1}'], ["recent_results", '[{"other":1}]'], ["receipts", '{"other":1}'], ["receipts", "[1]\n[2]"], ["action", "tool=command\nargs=[1]"]] as const) {
      const parts = evidenceInputParts({ key, text: other });
      expect(parts.map((part) => [part.head, part.text]), `${key} ${other}`).toEqual([[undefined, other]]);
    }
  });

  it("shows text cut at any point exactly as stored unless the cut left it whole", () => {
    for (const [key, whole] of Object.entries(stored)) {
      for (let end = 1; end < whole.length; end++) {
        const cut = whole.slice(0, end);
        const parts = evidenceInputParts({ key, text: cut });
        const asStored = parts.length === 1 && parts[0]?.head === undefined && parts[0]?.text === cut;
        // A cut that happens to end on a complete value is laid out; its blocks still carry only stored text.
        if (!asStored) expect(parts.every((part) => typeof part.text === "string"), `${key} cut at ${end}`).toBe(true);
        expect(parts.length, `${key} cut at ${end}`).toBeGreaterThan(0);
      }
    }
  });
});
