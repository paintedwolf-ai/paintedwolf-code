import { tags } from "@lezer/highlight";
import { denHighlightStyle } from "../editor/codemirror-highlight.generated.ts";
import { expect, it } from "vitest";
import { readerMarks, readerSecretSpans } from "./source-reader-spans.ts";

it("keeps one continuous change decoration across syntax boundaries", () => {
  const text = 'const café = "🙂";\n';
  const marks = readerMarks({ index: 0, end: 1, kind: "insert", text, before_line: 0, after_line: 1,
    changed: [{ from: 0, to: 10 }], syntax: [{ from: 0, to: 5, kind: "keyword" }, { from: 6, to: 10, kind: "variable" }] });
  expect(marks.filter(mark => mark.class === "cm-changedText")).toEqual([
    { from: 0, to: 10, class: "cm-changedText" },
  ]);
  expect(marks).toContainEqual({ from: 6, to: 10, class: denHighlightStyle.style([tags.variableName]) });
});

it("preserves secret facts at UTF-16 positions after a non-BMP character", () => {
  const row = { index: 0, end: 1, kind: "equal" as const, text: "🙂 token\n", before_line: 1, after_line: 1, changed: [],
    secret_screen: { truncated: false, spans: [{ start: 2, end: 7, state: "tracked" as const,
      rule_id: "credential", rule_title: "Credential", reference: "vault:token", shape: "token" }] } };
  expect(readerSecretSpans(row)).toEqual([{ from: 3, to: 8, state: "tracked", ruleId: "credential",
    ruleTitle: "Credential", reference: "vault:token", shape: "token" }]);
  expect(readerMarks(row)).toContainEqual({ from: 3, to: 8, class: "cm-den-secret cm-den-secret--tracked", title: "Credential" });
});
