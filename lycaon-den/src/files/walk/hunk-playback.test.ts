// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import {
  PLAYBACK_BURST_BUDGET_MS,
  PLAYBACK_EVENT_BUDGET_MS,
  applySettledDoc,
  computeLineHunks,
  hunkPlaybackExtension,
  playHunks,
  settleChange,
  settledContent,
} from "./hunk-playback.ts";

function makeView(doc: string): EditorView {
  const parent = document.createElement("div");
  document.body.appendChild(parent);
  return new EditorView({
    state: EditorState.create({
      doc,
      extensions: [hunkPlaybackExtension],
    }),
    parent,
  });
}

describe("hunk-playback", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    document.body.innerHTML = "";
  });
  afterEach(() => {
    vi.useRealTimers();
    document.body.innerHTML = "";
  });

  it("computes delete then insert hunks in document order", () => {
    const hunks = computeLineHunks("a\nb\nc\n", "a\nx\nc\n").filter(
      (h) => h.kind !== "equal",
    );
    expect(hunks.map((h) => h.kind)).toEqual(["delete", "insert"]);
  });

  it("settled content is always the after bytes (play ≡ swap)", () => {
    expect(settledContent("old", "new")).toBe("new");
    expect(settledContent("", "only")).toBe("only");
  });

  // Batches keep the animated property sweep within the test timeout.
  for (const [batch, initialSeed] of [["a", 0x5eed1e], ["b", 0xc0ffee]] as const) {
    it(`plays any pair of documents down to a plain swap (${batch})`, async () => {
      // Deterministic PRNG so a failure reproduces.
      let seed = initialSeed;
      const rand = () => {
        seed = (seed * 1664525 + 1013904223) >>> 0;
        return seed / 0x100000000;
      };
      const pick = <T>(xs: readonly T[]): T =>
        xs[Math.floor(rand() * xs.length)]!;
      const lines = ["", "a", "b", "  indented", "\ttab", "ünïcode", "x".repeat(40)];
      const makeDoc = () => {
        const n = Math.floor(rand() * 6);
        const body = Array.from({ length: n }, () => pick(lines)).join("\n");
        return rand() < 0.5 ? body : `${body}\n`;
      };

      // One view for every case: constructing a view per case costs more than
      // the playback under test.
      const view = makeView("");
      try {
        for (let i = 0; i < 100; i += 1) {
          const before = makeDoc();
          const after = makeDoc();
          view.dispatch({
            changes: { from: 0, to: view.state.doc.length, insert: before },
          });
          const session = playHunks(view, before, after, {
            reducedMotion: rand() < 0.5,
            visible: rand() < 0.5,
            onSettled: () => {},
          });
          await vi.runAllTimersAsync();
          await session.done;
          expect(view.state.doc.toString(), `${batch} step ${i}: ${JSON.stringify({ before, after })}`)
            .toBe(settledContent(before, after));
          expect(view.state.doc.toString()).toBe(after);
        }
      } finally {
        view.destroy();
      }
    });
  }

  it("identical bytes is a no-op", async () => {
    const view = makeView("same\n");
    const onSettled = vi.fn();
    const session = playHunks(view, "same\n", "same\n", { onSettled });
    await session.done;
    expect(onSettled).not.toHaveBeenCalled();
    expect(view.state.doc.toString()).toBe("same\n");
    view.destroy();
  });

  it("reduced-motion lands settled immediately", async () => {
    const view = makeView("a\n");
    const onSettled = vi.fn();
    const session = playHunks(view, "a\n", "b\n", {
      reducedMotion: true,
      onSettled,
    });
    await session.done;
    expect(onSettled).toHaveBeenCalledWith("b\n", expect.any(Object));
    expect(view.state.doc.toString()).toBe("b\n");
    view.destroy();
  });

  it("hidden buffer skips animation and settles", async () => {
    const view = makeView("a\n");
    const onSettled = vi.fn();
    const session = playHunks(view, "a\n", "b\n", {
      visible: false,
      onSettled,
    });
    await session.done;
    expect(view.state.doc.toString()).toBe("b\n");
    view.destroy();
  });

  it("does not settle a stale playback over newer local text", async () => {
    const view = makeView("local edit\n");
    const onSettled = vi.fn();
    const onDiscarded = vi.fn();
    const session = playHunks(view, "old disk\n", "new disk\n", {
      reducedMotion: true,
      canSettle: () => false,
      onDiscarded,
      onSettled,
    });
    await session.done;
    expect(view.state.doc.toString()).toBe("local edit\n");
    expect(onSettled).not.toHaveBeenCalled();
    expect(onDiscarded).toHaveBeenCalledOnce();
    view.destroy();
  });

  it("typing within defer window settles without animation steps", async () => {
    const view = makeView("a\n");
    const now = 10_000;
    const onSettled = vi.fn();
    const session = playHunks(view, "a\n", "b\n", {
      lastTypedAt: now - 500,
      now,
      onSettled,
    });
    await session.done;
    expect(onSettled).toHaveBeenCalledOnce();
    expect(view.state.doc.toString()).toBe("b\n");
    view.destroy();
  });

  it("budget overrun lands settled document immediately", async () => {
    const before = Array.from({ length: 40 }, (_, i) => `L${i}`).join("\n");
    const after = Array.from({ length: 40 }, (_, i) => `R${i}`).join("\n");
    const view = makeView(before);
    const onSettled = vi.fn();
    const session = playHunks(view, before, after, {
      eventBudgetMs: 1,
      burstRemainingMs: PLAYBACK_BURST_BUDGET_MS,
      onSettled,
    });
    await vi.advanceTimersByTimeAsync(PLAYBACK_EVENT_BUDGET_MS);
    await session.done;
    expect(view.state.doc.toString()).toBe(after);
    expect(onSettled).toHaveBeenCalledWith(after, expect.any(Object));
    view.destroy();
  });

  it("property: every settle path yields byte-exact after", async () => {
    const cases: Array<{
      name: string;
      opts: Parameters<typeof playHunks>[3];
    }> = [
      { name: "reduced", opts: { reducedMotion: true, onSettled: () => {} } },
      { name: "hidden", opts: { visible: false, onSettled: () => {} } },
      {
        name: "typing",
        opts: { lastTypedAt: Date.now(), now: Date.now(), onSettled: () => {} },
      },
      {
        name: "burst-exhausted",
        opts: { burstRemainingMs: 0, onSettled: () => {} },
      },
    ];
    for (const c of cases) {
      const view = makeView("before\n");
      let settled = "";
      const session = playHunks(view, "before\n", "after\n", {
        ...c.opts,
        onSettled: (text) => {
          settled = text;
        },
      });
      await session.done;
      expect(settled, c.name).toBe("after\n");
      expect(view.state.doc.toString(), c.name).toBe("after\n");
      view.destroy();
    }
  });

  it("narrows a settle to the differing range", () => {
    const before = `${"keep\n".repeat(200)}old\n${"tail\n".repeat(200)}`;
    const after = before.replace("old\n", "new\n");
    const doc = EditorState.create({ doc: before }).doc;

    const change = settleChange(doc, after);
    expect(change.insert).toBe("new");
    expect(change.to - change.from).toBe(3);

    // Applying it reproduces `after` byte for byte.
    expect(
      before.slice(0, change.from) + change.insert + before.slice(change.to),
    ).toBe(after);
  });

  it("settle change handles empty, identical, and multibyte edges", () => {
    const docOf = (text: string) => EditorState.create({ doc: text }).doc;
    const apply = (text: string, next: string) => {
      const c = settleChange(docOf(text), next);
      return text.slice(0, c.from) + c.insert + text.slice(c.to);
    };
    expect(apply("same\n", "same\n")).toBe("same\n");
    expect(apply("", "fresh\n")).toBe("fresh\n");
    expect(apply("gone\n", "")).toBe("");
    // Low-surrogate differences preserve whole code points.
    const c = settleChange(docOf("a😀b"), "a😁b");
    expect("a😀b".slice(0, c.from) + c.insert + "a😀b".slice(c.to)).toBe("a😁b");
    expect(c.insert).toBe("😁");
  });

  it("settles the document to after through the narrowed change", () => {
    const before = `${"keep\n".repeat(50)}old\n`;
    const after = before.replace("old\n", "new\n");
    const view = makeView(before);
    applySettledDoc(view, after, { line: 1, col: 1 });
    expect(view.state.doc.toString()).toBe(after);
    view.destroy();
  });

  it("puts the settle cursor on the clamped line and column", () => {
    const view = makeView("a\nb\nc\n");
    applySettledDoc(view, "aa\nbb\ncc\n", { line: 2, col: 2 });
    const head = view.state.selection.main.head;
    const line = view.state.doc.lineAt(head);
    expect(line.number).toBe(2);
    expect(head - line.from + 1).toBe(2);

    // Past the end clamps instead of throwing.
    applySettledDoc(view, "x\n", { line: 99, col: 99 });
    expect(view.state.selection.main.head).toBeLessThanOrEqual(
      view.state.doc.length,
    );
    view.destroy();
  });

  it("animated path settles to after after timers drain", async () => {
    const view = makeView("a\nb\n");
    const onSettled = vi.fn();
    const session = playHunks(view, "a\nb\n", "a\nx\n", {
      onSettled,
      eventBudgetMs: 5_000,
      burstRemainingMs: 5_000,
    });
    await vi.advanceTimersByTimeAsync(5_000);
    await session.done;
    expect(view.state.doc.toString()).toBe("a\nx\n");
    expect(onSettled).toHaveBeenCalledWith("a\nx\n", expect.any(Object));
    view.destroy();
  });

  it("settles before animation and never replaces typing from later frames", async () => {
    const view = makeView("before\n");
    const session = playHunks(view, "before\n", "after\n", { onSettled: vi.fn(), eventBudgetMs: 5_000 });
    expect(view.state.doc.toString()).toBe("after\n");
    view.dispatch({ changes: { from: 0, insert: "typed " }, userEvent: "input.type" });
    await vi.advanceTimersByTimeAsync(5_000);
    await session.done;
    expect(view.state.doc.toString()).toBe("typed after\n");
    view.destroy();
  });

  it("settles the completion promise when animation is cancelled", async () => {
    const view = makeView("before\n");
    const session = playHunks(view, "before\n", "after\n", { onSettled: vi.fn() });
    session.cancel();
    await session.done;
    expect(view.state.doc.toString()).toBe("after\n");
    view.destroy();
  });
});
