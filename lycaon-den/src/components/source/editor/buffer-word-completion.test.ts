import { EditorState } from "@codemirror/state";
import { CompletionContext } from "@codemirror/autocomplete";
import { beforeEach, describe, expect, it } from "vitest";
import {
  bufferWordSource,
  resetBufferWordCacheForTests,
} from "./buffer-word-completion.ts";

function complete(doc: string, pos: number) {
  const state = EditorState.create({ doc });
  const ctx = new CompletionContext(state, pos, false);
  return bufferWordSource(ctx);
}

describe("bufferWordSource", () => {
  beforeEach(() => resetBufferWordCacheForTests());

  it("offers candidates of at least 3 characters", () => {
    const doc = "alpha beta ab alphabet\n";
    // Cursor after "al"
    const result = complete(doc + "al", doc.length + 2);
    expect(result).not.toBeNull();
    const labels = result!.options.map((o) => o.label);
    expect(labels).toContain("alpha");
    expect(labels).toContain("alphabet");
    expect(labels.every((l) => l.length >= 3)).toBe(true);
  });

  it("excludes the token currently being typed", () => {
    const prefix = "hello hello world\nhel";
    const result = complete(prefix, prefix.length);
    expect(result).not.toBeNull();
    const labels = result!.options.map((o) => o.label);
    expect(labels).toContain("hello");
    expect(labels).not.toContain("hel");
  });

  it("ignores words outside the scan window around the cursor", () => {
    // Filler comfortably exceeds the scan radius, so the leading token is out
    // of range while the trailing one is not.
    const doc = `farAwayToken\n${"yyy\n".repeat(12_000)}farNearToken\nfar`;
    const result = complete(doc, doc.length);
    expect(result).not.toBeNull();
    const labels = result!.options.map((o) => o.label);
    expect(labels).toContain("farNearToken");
    expect(labels).not.toContain("farAwayToken");
  });

  it("re-scans when an edit leaves the document length unchanged", () => {
    const pad = "x".repeat(40);
    const before = `${pad}\nalphaWord\n${pad}\nal`;
    const after = `${pad}\nbetaWordd\n${pad}\nal`;
    expect(after.length).toBe(before.length);

    const first = complete(before, before.length);
    expect(first!.options.map((o) => o.label)).toContain("alphaWord");

    // No cache reset: only the middle changed, so a length-and-edges
    // fingerprint would have served the stale hit list here.
    const second = complete(after, after.length);
    const labels = second?.options.map((o) => o.label) ?? [];
    expect(labels).not.toContain("alphaWord");
  });

  it("ranks proximity ahead of frequency", () => {
    // Both share prefix "co"; the nearer spelling wins even if rarer.
    const far = "commonFar commonFar commonFar\n";
    const near = "colocated\nco";
    const doc = far + near;
    const result = complete(doc, doc.length);
    expect(result).not.toBeNull();
    const labels = result!.options.map((o) => o.label);
    expect(labels[0]).toBe("colocated");
    expect(labels).toContain("commonFar");
  });
});
