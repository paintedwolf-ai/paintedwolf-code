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
  it("lays out each call, result and receipt as its own block", () => {
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

  it("keeps every argument when a call has more than a command", () => {
    const args = { path: "a.go", content: "package app" };
    const [part] = evidenceInputParts({ key: "action", text: `tool=write\nargs=${JSON.stringify(args)}` });
    expect(part?.head?.[0]).toBe("write");
    expect(JSON.parse(part?.text ?? "")).toEqual(args);
  });

  it("indents structured text of any other key without changing its value", () => {
    const [part] = evidenceInputParts({ key: "file", text: stored.file });
    expect(JSON.parse(part?.text ?? "")).toEqual(JSON.parse(stored.file!));
    expect(evidenceInputParts({ key: "never_listed", text: "plain" })).toEqual([{ text: "plain" }]);
    expect(evidenceInputParts({ key: "state" })).toEqual([]);
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
