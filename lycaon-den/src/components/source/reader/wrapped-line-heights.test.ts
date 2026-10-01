// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { EditorState, Text } from "@codemirror/state";
import { EditorView } from "@codemirror/view";

// The vendored editor patch: a region the editor has not drawn is estimated
// from the rows each of its lines wraps to, not from its character total.
type HeightOracleView = {
  viewState: {
    heightOracle: {
      lineWrapping: boolean;
      lineLength: number;
      lineHeight: number;
      setDoc: (doc: Text) => void;
      heightForGap: (from: number, to: number) => number;
    };
  };
};

function wrapped(doc: string): EditorView {
  return new EditorView({
    parent: document.createElement("div"),
    state: EditorState.create({ doc, extensions: [EditorView.lineWrapping] }),
  });
}

function oracleOf(view: EditorView) {
  return (view as unknown as HeightOracleView).viewState.heightOracle;
}

describe("wrapped height estimate", () => {
  it("counts the rows each undrawn line wraps to", () => {
    const empty = wrapped("");
    const { lineHeight, lineLength } = oracleOf(empty);
    empty.destroy();

    const rows = (line: string) => Math.max(1, Math.ceil(line.length / lineLength));

    for (const line of ["x".repeat(Math.floor(lineLength / 2)), "x".repeat(lineLength * 3)]) {
      const view = wrapped(Array.from({ length: 40 }, () => line).join("\n"));
      try {
        expect(view.contentHeight).toBeCloseTo(40 * rows(line) * lineHeight, 0);
      } finally {
        view.destroy();
      }
    }
  });

  it("places a line inside an undrawn region by the rows above it", () => {
    const long = "x".repeat(90);
    const view = wrapped(Array.from({ length: 40 }, () => long).join("\n"));
    try {
      const { lineHeight, lineLength } = oracleOf(view);
      const rowsPerLine = Math.max(1, Math.ceil(long.length / lineLength));
      const line = view.state.doc.line(20);
      expect(view.lineBlockAt(line.from).top).toBeCloseTo(
        19 * rowsPerLine * lineHeight,
        0,
      );
    } finally {
      view.destroy();
    }
  });

  it("answers a height with the line whose rows reach it", () => {
    const long = "x".repeat(90);
    const view = wrapped(Array.from({ length: 40 }, () => long).join("\n"));
    try {
      const { lineHeight, lineLength } = oracleOf(view);
      const rowsPerLine = Math.max(1, Math.ceil(long.length / lineLength));
      const lineTop = 10 * rowsPerLine * lineHeight;
      const block = view.lineBlockAtHeight(lineTop + lineHeight / 2);
      expect(view.state.doc.lineAt(block.from).number).toBe(11);
      expect(block.top).toBeCloseTo(lineTop, 0);
      expect(block.height).toBeCloseTo(rowsPerLine * lineHeight, 0);
    } finally {
      view.destroy();
    }
  });

  it("walks the lines of an undrawn region in row order", () => {
    const view = wrapped(
      Array.from({ length: 40 }, (_, index) => "x".repeat(index * 12)).join("\n"),
    );
    try {
      const { lineHeight, lineLength } = oracleOf(view);
      const blocks = view.viewportLineBlocks;
      expect(blocks.length).toBeGreaterThan(1);
      for (const block of blocks) {
        const line = view.state.doc.lineAt(block.from);
        const rows = Math.max(1, Math.ceil(line.length / lineLength));
        expect(block.height).toBeCloseTo(rows * lineHeight, 0);
      }
      for (let index = 1; index < blocks.length; index++) {
        expect(blocks[index]!.top).toBeCloseTo(
          blocks[index - 1]!.top + blocks[index - 1]!.height,
          0,
        );
      }
    } finally {
      view.destroy();
    }
  });

  it("recomputes when the document content or the row width changes", () => {
    const view = wrapped("short\nshort");
    const oracle = oracleOf(view);
    const original = view.state.doc;
    const changed = Text.of(["x".repeat(25), "short"]);
    try {
      oracle.lineLength = 10;
      oracle.lineHeight = 10;
      oracle.setDoc(original);
      expect(oracle.heightForGap(0, original.length)).toBe(20);

      oracle.setDoc(changed);
      expect(oracle.heightForGap(0, changed.length)).toBe(40);
      oracle.lineLength = 5;
      expect(oracle.heightForGap(0, changed.length)).toBe(60);

      oracle.setDoc(original);
      expect(oracle.heightForGap(0, original.length)).toBe(20);
    } finally {
      view.destroy();
    }
  });

  it("reads the same as a fresh editor whatever it cached before", () => {
    const cached = wrapped("");
    const held = oracleOf(cached);
    try {
      for (const width of [1, 5, 13, 30, 80]) {
        for (const count of [1, 2, 9, 40]) {
          for (const shape of [0, 1, 2]) {
            const lines = Array.from({ length: count }, (_, index) =>
              "x".repeat(shape === 0 ? 0 : shape === 1
                ? width + index * 7 + 1 : (index % 3) * width));
            const fresh = wrapped(lines.join("\n"));
            try {
              const cold = oracleOf(fresh);
              for (const estimate of [held, cold]) {
                estimate.lineWrapping = shape !== 1;
                estimate.lineLength = width;
                estimate.lineHeight = shape === 2 ? 19.5 : 7;
                estimate.setDoc(fresh.state.doc);
              }
              const middle = fresh.state.doc.line(Math.ceil(count / 2));
              const ranges: Array<[number, number]> = [
                [0, fresh.state.doc.length],
                [middle.from, fresh.state.doc.length],
                [middle.from, middle.to],
              ];
              for (const [from, to] of ranges) {
                expect(held.heightForGap(from, to)).toBe(cold.heightForGap(from, to));
              }
            } finally {
              fresh.destroy();
            }
          }
        }
      }
    } finally {
      cached.destroy();
    }
  });
});
