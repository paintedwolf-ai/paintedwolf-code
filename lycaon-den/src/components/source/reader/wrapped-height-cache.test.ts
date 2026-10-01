// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { createRequire } from "node:module";
import { EditorView } from "@codemirror/view";
import type { Text } from "@codemirror/state";

type HeightOracle = {
  lineWrapping: boolean;
  lineLength: number;
  lineHeight: number;
  setDoc: (doc: Text) => void;
  heightForGap: (from: number, to: number) => number;
};

function heightOracle(view: EditorView): HeightOracle {
  return (view as unknown as { viewState: { heightOracle: HeightOracle } }).viewState.heightOracle;
}

const { EditorView: CommonJSView } = createRequire(import.meta.url)("@codemirror/view") as {
  EditorView: typeof EditorView;
};

describe.each([
  ["ES module", EditorView],
  ["CommonJS", CommonJSView],
] as const)("%s wrapped height cache", (_name, View) => {
  function wrapped(doc: string | Text): EditorView {
    return new View({ doc, extensions: [View.lineWrapping] });
  }

  it("recomputes for content edits that preserve the line count", () => {
    const cached = wrapped("short\nshort");
    const replacement = wrapped("x".repeat(25) + "\nshort");
    const held = heightOracle(cached);
    const original = cached.state.doc;
    const changed = replacement.state.doc;
    try {
      held.lineLength = 10;
      held.lineHeight = 10;
      held.setDoc(original);
      expect(held.heightForGap(0, original.length)).toBe(20);
      held.setDoc(changed);
      expect(held.heightForGap(0, changed.length)).toBe(40);
      held.lineLength = 5;
      expect(held.heightForGap(0, changed.length)).toBe(60);
      held.setDoc(original);
      expect(held.heightForGap(0, original.length)).toBe(20);
    } finally {
      replacement.destroy();
      cached.destroy();
    }
  });

  it("matches fresh height estimates when only geometry changes", () => {
    const cached = wrapped("short\n" + "x".repeat(67) + "\n" + "x".repeat(151));
    const held = heightOracle(cached);
    const doc = cached.state.doc;
    try {
      for (const width of [1, 5, 13, 30, 80]) {
        for (const wrapping of [true, false]) {
          for (const height of [7, 19.5]) {
            const fresh = wrapped(doc);
            try {
              const cold = heightOracle(fresh);
              for (const estimate of [held, cold]) {
                estimate.lineWrapping = wrapping;
                estimate.lineLength = width;
                estimate.lineHeight = height;
                estimate.setDoc(doc);
              }
              const middle = doc.line(2);
              expect(held.heightForGap(0, doc.length)).toBe(cold.heightForGap(0, doc.length));
              expect(held.heightForGap(middle.from, middle.to)).toBe(cold.heightForGap(middle.from, middle.to));
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
