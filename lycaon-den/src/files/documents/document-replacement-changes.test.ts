import { ChangeSet, Text } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import * as Y from "yjs";
import { documentReplacementChanges } from "./document-replacement-changes.ts";

describe("replacement identity preservation", () => {
  const samples = ["", "a", "a\nb\n", "🐺 café\n日本語\n", "same\nsame\n", "\r\n", "🐺", "🦊"];
  it("preserves exact results and complete Unicode characters across a replacement matrix", () => {
    for (const before of samples) for (const after of samples) {
      const doc = Text.of(before.split("\n"));
      const replacement = ChangeSet.of({ from: 0, to: doc.length, insert: after }, doc.length);
      const changes = documentReplacementChanges(doc, replacement);
      expect(changes.apply(doc).toString()).toBe(replacement.apply(doc).toString());
      changes.iterChanges((from, to, _a, _b, insert) => {
        for (const text of [before.slice(0, from), before.slice(to), insert.toString()]) {
          expect(Array.from(text).some((char) => /^[\uD800-\uDFFF]$/.test(char))).toBe(false);
        }
      });
    }
  });

  it.each([false, true])("converges independent edits inside and outside broad replacements (local first=%s)", (localFirst) => {
    const base = "first: old\nunchanged 🐺\nlast: old\n";
    const after = "first: new\nunchanged 🐺\nlast: new\n";
    const original = new Y.Doc({ gc: false });
    original.getText("text").insert(0, base);
    const left = new Y.Doc({ gc: false });
    const right = new Y.Doc({ gc: false });
    for (const peer of [left, right]) Y.applyUpdate(peer, Y.encodeStateAsUpdate(original));
    const doc = Text.of(base.split("\n"));
    const changes = documentReplacementChanges(doc, ChangeSet.of({ from: 0, to: doc.length, insert: after }, doc.length));
    let delta = 0;
    left.transact(() => changes.iterChanges((from, to, _a, _b, insert) => {
      left.getText("text").delete(from + delta, to - from);
      left.getText("text").insert(from + delta, insert.toString());
      delta += insert.length - (to - from);
    }));
    right.getText("text").insert(base.indexOf("🐺"), "external ");
    for (const peer of localFirst ? [left, right] : [right, left]) Y.applyUpdate(original, Y.encodeStateAsUpdate(peer));
    expect(original.getText("text").toString()).toBe(after.replace("🐺", "external 🐺"));
    for (const peer of [original, left, right]) peer.destroy();
  });
});
