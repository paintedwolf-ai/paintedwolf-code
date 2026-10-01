import { render, cleanup } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createReaderFacts } from "./source-reader-facts.tsx";
import type { ReaderEditor } from "./source-reader-editor.ts";
import type { SourceReaderRow } from "../../../api/types.ts";

function createMockCell() {
  const cell = document.createElement("div");
  cell.className = "cm-gutterElement";
  const button = document.createElement("button");
  button.className = "files-line-gutter__facts";
  button.setAttribute("aria-haspopup", "dialog");
  button.setAttribute("aria-expanded", "false");
  cell.appendChild(button);
  document.body.appendChild(cell);
  return { cell, button };
}

function createMockEditor(rowOverride?: (line: number) => SourceReaderRow | undefined) {
  return {
    document: {
      at: (pos: number) => {
        const line = pos / 10;
        const row = rowOverride
          ? rowOverride(line)
          : ({
              after_line: line,
              before_line: line,
              contributors: [{ session_id: `session-${line}`, tool_call_id: `call-${line}` }],
            } as SourceReaderRow);
        return row ? { row } : undefined;
      },
    },
    view: {
      state: {
        doc: {
          line: (line: number) => ({ from: line * 10 }),
        },
      },
    },
  } as unknown as ReaderEditor;
}

afterEach(() => {
  cleanup();
  document.body.replaceChildren();
});

describe("createReaderFacts", () => {
  it("clears aria-expanded and is-open on previous anchor when hovering a new line", () => {
    const editor = createMockEditor();
    const cell1 = createMockCell();
    const cell2 = createMockCell();

    let facts!: ReturnType<typeof createReaderFacts>;
    render(() => {
      facts = createReaderFacts(() => "test.ts", vi.fn());
      return facts.card;
    });

    facts.show(editor, 1, cell1.cell, false);
    expect(cell1.cell.classList.contains("is-open")).toBe(true);
    expect(cell1.button.getAttribute("aria-expanded")).toBe("true");

    facts.show(editor, 2, cell2.cell, false);
    expect(cell1.cell.classList.contains("is-open")).toBe(false);
    expect(cell1.button.getAttribute("aria-expanded")).toBe("false");
    expect(cell2.cell.classList.contains("is-open")).toBe(true);
    expect(cell2.button.getAttribute("aria-expanded")).toBe("true");
  });

  it("clears aria-expanded when moving to a line without contributors", () => {
    const editor = createMockEditor((line) =>
      line === 1
        ? ({
            after_line: 1,
            before_line: 1,
            contributors: [{ session_id: "s1" }],
          } as SourceReaderRow)
        : undefined,
    );
    const cell1 = createMockCell();
    const cell2 = createMockCell();

    let facts!: ReturnType<typeof createReaderFacts>;
    render(() => {
      facts = createReaderFacts(() => "test.ts", vi.fn());
      return facts.card;
    });

    facts.show(editor, 1, cell1.cell, false);
    expect(cell1.button.getAttribute("aria-expanded")).toBe("true");

    facts.show(editor, 2, cell2.cell, false);
    expect(cell1.cell.classList.contains("is-open")).toBe(false);
    expect(cell1.button.getAttribute("aria-expanded")).toBe("false");
  });
});
