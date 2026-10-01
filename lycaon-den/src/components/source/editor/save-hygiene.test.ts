import { describe, expect, it } from "vitest";
import { ChangeSet, Text, type ChangeSpec } from "@codemirror/state";
import { saveHygieneChanges } from "./save-hygiene.ts";

const doc = (text: string) => Text.of(text.split("\n"));
const apply = (text: string, changes: ChangeSpec[]) => ChangeSet.of(changes, text.length).apply(doc(text)).toString();

describe("saveHygieneChanges", () => {
  it("preserves text when EditorConfig is absent", () => {
    expect(saveHygieneChanges(doc("a  \nb\n"), "lf", undefined)).toEqual({ changes: [], eol: "lf" });
  });

  it("trims each trailing run and the final newline as their own edits", () => {
    const text = "a  \nb\t\nc";
    const { changes, eol } = saveHygieneChanges(doc(text), "lf", { trimTrailingWhitespace: true, insertFinalNewline: true });
    expect(changes).toEqual([{ from: 1, to: 3 }, { from: 5, to: 6 }, { from: 8, insert: "\n" }]);
    expect(apply(text, changes)).toBe("a\nb\nc\n");
    expect(eol).toBe("lf");
  });

  it("inserts a final newline on non-empty files only", () => {
    expect(apply("hello", saveHygieneChanges(doc("hello"), "lf", { insertFinalNewline: true }).changes)).toBe("hello\n");
    expect(saveHygieneChanges(doc(""), "lf", { insertFinalNewline: true }).changes).toEqual([]);
    expect(saveHygieneChanges(doc("a\nb\n"), "lf", { trimTrailingWhitespace: true, insertFinalNewline: true }).changes).toEqual([]);
  });

  it("strips every final newline when insert_final_newline is false", () => {
    expect(apply("hello\n\n", saveHygieneChanges(doc("hello\n\n"), "lf", { insertFinalNewline: false }).changes)).toBe("hello");
  });

  it("selects end_of_line without editing the body", () => {
    expect(saveHygieneChanges(doc("a\nb\n"), "lf", { endOfLine: "crlf" })).toEqual({ changes: [], eol: "crlf" });
  });
});
