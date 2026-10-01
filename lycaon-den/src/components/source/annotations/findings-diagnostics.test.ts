// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { diagnosticCount } from "@codemirror/lint";
import type { FindingLevel, SecurityFinding } from "../../../api/types.ts";
import {
  findingDiagnostics,
  setFindingDiagnostics,
} from "./findings-diagnostics.ts";

const DOC = [
  "package main",
  "",
  "func main() {",
  "    exec.Command(userInput)",
  "}",
].join("\n");

function finding(over: Partial<SecurityFinding> = {}): SecurityFinding {
  return {
    rule_id: "lycaon.go.command-injection",
    level: "high",
    message: "User input reaches a command",
    locations: [{ uri: "main.go", start_line: 4 }],
    fingerprints: { primary: "fp-1" },
    tool: { driver_id: "opengrep", name: "OpenGrep" },
    ...over,
  };
}

function docOf(text = DOC) {
  return EditorState.create({ doc: text }).doc;
}

describe("findings-diagnostics", () => {
  it("maps finding levels onto diagnostic severities", () => {
    const levels: FindingLevel[] = ["critical", "high", "medium", "low", "info", "unknown"];
    const out = findingDiagnostics(
      "main.go",
      levels.map((level) => finding({ level })),
      docOf(),
    );
    expect(out.map((d) => d.severity)).toEqual([
      "error",
      "error",
      "warning",
      "info",
      "hint",
      "hint",
    ]);
    // The Den level survives the narrowing on the class the theme colors.
    expect(out.map((d) => d.markClass)).toEqual(
      levels.map((l) => `cm-den-finding cm-den-finding-${l}`),
    );
  });

  it("underlines the reported span when the scan gives columns", () => {
    const doc = docOf();
    const [d] = findingDiagnostics(
      "main.go",
      [
        finding({
          locations: [
            {
              uri: "main.go",
              start_line: 4,
              start_column: 5,
              end_line: 4,
              end_column: 17,
            },
          ],
        }),
      ],
      doc,
    );
    const line = doc.line(4);
    // Columns are 1-based and the end column is exclusive, per SARIF.
    expect(d!.from).toBe(line.from + 4);
    expect(d!.to).toBe(line.from + 16);
    expect(doc.sliceString(d!.from, d!.to)).toBe("exec.Command");
  });

  it("underlines the line's content, not its indentation, without columns", () => {
    const doc = docOf();
    const [d] = findingDiagnostics("main.go", [finding()], doc);
    const line = doc.line(4);
    expect(d!.from).toBe(line.from + 4);
    expect(d!.to).toBe(line.to);
  });

  it("widens a zero-width report to the end of the line", () => {
    const doc = docOf();
    const [d] = findingDiagnostics(
      "main.go",
      [
        finding({
          locations: [
            {
              uri: "main.go",
              start_line: 3,
              start_column: 6,
              end_line: 3,
              end_column: 6,
            },
          ],
        }),
      ],
      doc,
    );
    expect(d!.to).toBe(doc.line(3).to);
    expect(d!.to).toBeGreaterThan(d!.from);
  });

  it("spans multiple lines when the report does", () => {
    const doc = docOf();
    const [d] = findingDiagnostics(
      "main.go",
      [
        finding({
          locations: [
            { uri: "main.go", start_line: 3, start_column: 1, end_line: 5, end_column: 2 },
          ],
        }),
      ],
      doc,
    );
    expect(doc.lineAt(d!.from).number).toBe(3);
    expect(doc.lineAt(d!.to).number).toBe(5);
  });

  it("drops a location past the end of the buffer", () => {
    expect(
      findingDiagnostics(
        "main.go",
        [finding({ locations: [{ uri: "main.go", start_line: 900 }] })],
        docOf(),
      ),
    ).toEqual([]);
  });

  it("keeps findings on their normalized root-relative path", () => {
    const hits = findingDiagnostics(
      "cmd/app/main.go",
      [finding({ locations: [{ uri: "repo/cmd/app/main.go", start_line: 4 }] })],
      docOf(),
    );
    expect(hits).toEqual([]);
  });

  it("ignores findings that belong to another file", () => {
    expect(
      findingDiagnostics(
        "main.go",
        [finding({ locations: [{ uri: "other/helper.go", start_line: 4 }] })],
        docOf(),
      ),
    ).toEqual([]);
  });

  it("carries the Den level and the scanner on every diagnostic", () => {
    const [d] = findingDiagnostics(
      "main.go",
      [finding({ level: "critical" })],
      docOf(),
    );
    expect(d!.severity).toBe("error");
    expect(d!.markClass).toBe("cm-den-finding cm-den-finding-critical");
    expect(d!.source).toBe("OpenGrep · lycaon.go.command-injection");
    expect(d!.message).toBe("Critical: User input reaches a command");
  });

  it("orders diagnostics down the document, not by scan order", () => {
    const doc = docOf();
    const out = findingDiagnostics(
      "main.go",
      [
        finding({ locations: [{ uri: "main.go", start_line: 5 }] }),
        finding({ locations: [{ uri: "main.go", start_line: 1 }] }),
        finding({ locations: [{ uri: "main.go", start_line: 3 }] }),
      ],
      doc,
    );
    expect(out.map((d) => doc.lineAt(d.from).number)).toEqual([1, 3, 5]);
  });

  it("offers no actions when the host wires no handlers", () => {
    const [d] = findingDiagnostics("main.go", [finding()], docOf());
    expect(d!.actions ?? []).toEqual([]);
  });

  it("selects the reported span before handing off to the fix verb", () => {
    const doc = docOf();
    const onFix = vi.fn();
    const onOpen = vi.fn();
    const f = finding();
    const [d] = findingDiagnostics("main.go", [f], doc, { onOpen, onFix });
    expect(d!.actions?.map((a) => a.name)).toEqual([
      "Open finding",
      "Fix this finding",
    ]);

    const dispatch = vi.fn();
    const focus = vi.fn();
    const view = { dispatch, focus } as unknown as EditorView;
    d!.actions![1]!.apply(view, d!.from, d!.to);
    expect(dispatch).toHaveBeenCalledWith({
      selection: { anchor: d!.from, head: d!.to },
    });
    expect(focus).toHaveBeenCalled();
    expect(onFix).toHaveBeenCalled();

    d!.actions![0]!.apply(view, d!.from, d!.to);
    expect(onOpen).toHaveBeenCalledWith(f);
  });

  it("installs and then clears diagnostics on a live view", () => {
    const view = new EditorView({ state: EditorState.create({ doc: DOC }) });
    try {
      setFindingDiagnostics(view, "main.go", [finding()]);
      expect(diagnosticCount(view.state)).toBe(1);
      setFindingDiagnostics(view, "main.go", []);
      expect(diagnosticCount(view.state)).toBe(0);
    } finally {
      view.destroy();
    }
  });
});
