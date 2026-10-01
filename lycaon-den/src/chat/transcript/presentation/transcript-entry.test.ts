// @vitest-environment jsdom
import { fileEditPreviewFixture } from "../../../test/file-edit-fixture.ts";
import { createRoot, createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  TRANSCRIPT_ENTER_FADE_ANIMATION,
  TRANSCRIPT_ENTER_FADE_MS,
} from "./transcript-entry-fade.ts";
import {
  clearTranscriptEntryMemory,
  hasTranscriptEntryMemory,
  noteTranscriptEntryBaseline,
  rememberTranscriptEntry,
  activitySpanEnterFadeKey,
  transcriptEntryKeysFromMessages,
  useTranscriptEntry,
} from "./transcript-entry.ts";
import { DEN_SCROLLING_ATTR } from "../../../platform/scrolling/scroll-activity.ts";

const FADE_CLASS = "den-enter-fade";

afterEach(() => {
  clearTranscriptEntryMemory();
});

describe("transcript entry memory", () => {
  it("remembers keys per session so remounts do not replay", () => {
    rememberTranscriptEntry("s1", "tc-1");
    expect(hasTranscriptEntryMemory("s1", "tc-1")).toBe(true);
    expect(hasTranscriptEntryMemory("s2", "tc-1")).toBe(false);
  });

  it("baselines hydrated rows as already entered", () => {
    noteTranscriptEntryBaseline("s1", ["u1", "tc-1"]);
    expect(hasTranscriptEntryMemory("s1", "u1")).toBe(true);
    expect(hasTranscriptEntryMemory("s1", "tc-2")).toBe(false);
  });

  it("clears a single session without touching others", () => {
    rememberTranscriptEntry("s1", "a");
    rememberTranscriptEntry("s2", "b");
    clearTranscriptEntryMemory("s1");
    expect(hasTranscriptEntryMemory("s1", "a")).toBe(false);
    expect(hasTranscriptEntryMemory("s2", "b")).toBe(true);
  });

  it("does not baseline bare wire tool_call_id from transcript rows", () => {
    const keys = transcriptEntryKeysFromMessages([
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "call_1", name: "read", args: {} }],
        created_at: "t",
        seq: 1,
      },
      {
        id: "t1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        tool_result: {
          assistant_message_id: "a1",
          tool_call_id: "call_1",
          tool: "read",
          content: "",
        },
        content: "ok",
        created_at: "t",
        seq: 2,
      },
    ]);
    expect(keys).toContain("a1:call_1");
    expect(keys).not.toContain("call_1");
  });

  it("activity span enter key is distinct from the lead row key", () => {
    expect(activitySpanEnterFadeKey("a1:call_0")).toBe("a1:call_0:activity-enter");
    rememberTranscriptEntry("s1", "a1:call_0");
    expect(hasTranscriptEntryMemory("s1", activitySpanEnterFadeKey("a1:call_0"))).toBe(
      false,
    );
  });

  it("baselines activity_span fade keys so hydrated collapses do not replay", () => {
    const keys = transcriptEntryKeysFromMessages([
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [
          { id: "c1", name: "terminal_open", args: { command: "python" } },
          { id: "c2", name: "terminal_send", args: { id: "pty-1", data: "x" } },
        ],
        created_at: "t",
        seq: 1,
      },
      {
        id: "t1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        tool_result: {
          assistant_message_id: "a1",
          tool_call_id: "c1",
          tool: "terminal_open",
          content: "ok",
        },
        content: "ok",
        created_at: "t",
        seq: 2,
      },
      {
        id: "t2",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        tool_result: {
          assistant_message_id: "a1",
          tool_call_id: "c2",
          tool: "terminal_send",
          content: "ok",
        },
        content: "ok",
        created_at: "t",
        seq: 3,
      },
    ]);
    expect(keys).toContain("a1:c1");
    expect(keys).toContain(activitySpanEnterFadeKey("a1:c1"));
  });

  it("baselines secondary entry keys for file edits, visuals, and evidence", () => {
    const keys = transcriptEntryKeysFromMessages([
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "c1", name: "write", args: { path: "a.go", content: "x" } }],
        created_at: "t",
        seq: 1,
      },
      {
        id: "t1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        tool_result: {
          assistant_message_id: "a1",
          tool_call_id: "c1",
          tool: "write",
          content: "ok",
          file_edit_preview: fileEditPreviewFixture({ path: "a.go", before: "", after: "x" }),
        },
        content: "ok",
        created_at: "t",
        seq: 2,
      },
      {
        id: "a2",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "done",
        visibility: "transcript",
        grounding: {
          traced: true,
          hint_code: "grounded",
          checks: [],
        },
        created_at: "t",
        seq: 3,
      },
      {
        id: "b1",
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        kind: "workflow_boundary",
        content: "",
        workflow_boundary: { event: "paused", phase: "review" },
        created_at: "t",
        seq: 4,
      },
    ]);
    expect(keys).toContain("a1:c1:file-edit");
    expect(keys).toContain("a2:evidence");
    expect(keys).toContain("b1");
  });
});

describe("useTranscriptEntry", () => {
  const disposers: Array<() => void> = [];
  afterEach(() => {
    while (disposers.length) disposers.pop()?.();
  });

  /** The hook retains subscriptions even when its ref binds later. */
  const bindReactive = (
    node: HTMLElement,
    opts: () => { sessionId?: string; entryKey?: string; skipFade?: boolean },
  ) => {
    createRoot((dispose) => {
      disposers.push(dispose);
      const { bindTranscriptEntry } = useTranscriptEntry(opts);
      bindTranscriptEntry(node);
    });
  };

  const bind = (
    node: HTMLElement,
    opts: { sessionId?: string; entryKey?: string; skipFade?: boolean },
  ) => bindReactive(node, () => opts);

  it.each(["same node", "replacement node", "unbound node"])(
    "retires delayed ref bindings through disposal: %s",
    (variant) => {
      const [key, setKey] = createSignal("initial");
      let dispose!: () => void;
      const binding = createRoot((stop) => {
        dispose = stop;
        disposers.push(stop);
        return useTranscriptEntry(() => ({ sessionId: "delayed", entryKey: key() }));
      });
      const first = document.createElement("div");
      binding.bindTranscriptEntry(first);
      expect(hasTranscriptEntryMemory("delayed", "initial")).toBe(true);
      const next = variant === "replacement node" ? document.createElement("div") : first;
      binding.bindTranscriptEntry(variant === "unbound node" ? undefined : next);
      setKey("updated");
      expect(hasTranscriptEntryMemory("delayed", "updated")).toBe(variant !== "unbound node");
      dispose();
      setKey("after-disposal");
      expect(hasTranscriptEntryMemory("delayed", "after-disposal")).toBe(false);
    },
  );

  it("fades a fresh live row in once and remembers its key", () => {
    const el = document.createElement("div");
    bind(el, { sessionId: "s1", entryKey: "u-live" });
    expect(el.classList.contains(FADE_CLASS)).toBe(true);
    expect(hasTranscriptEntryMemory("s1", "u-live")).toBe(true);
  });

  it("records skipFade rows without playing the arrival fade", () => {
    const el = document.createElement("div");
    bind(el, { sessionId: "s1", entryKey: "u-instant", skipFade: true });
    expect(el.classList.contains(FADE_CLASS)).toBe(false);
    expect(hasTranscriptEntryMemory("s1", "u-instant")).toBe(true);
  });

  it("mounts rows entering through an active scroll without an arrival fade", () => {
    const scroller = document.createElement("div");
    scroller.setAttribute(DEN_SCROLLING_ATTR, "");
    const el = document.createElement("div");
    scroller.append(el);
    bind(el, { sessionId: "s1", entryKey: "history-through-scroll" });

    expect(el.classList.contains(FADE_CLASS)).toBe(false);
    expect(hasTranscriptEntryMemory("s1", "history-through-scroll")).toBe(true);
  });

  it("checks the scroll ancestor after the initial ref is inserted", () => {
    const scroller = document.createElement("div");
    scroller.setAttribute(DEN_SCROLLING_ATTR, "");
    const row = document.createElement("div");
    createRoot((dispose) => {
      disposers.push(dispose);
      const binding = useTranscriptEntry(() => ({ sessionId: "scroll", entryKey: "inserted" }));
      binding.bindTranscriptEntry(row);
      scroller.append(row);
    });
    expect(hasTranscriptEntryMemory("scroll", "inserted")).toBe(true);
    expect(row.classList.contains(FADE_CLASS)).toBe(false);
  });

  it("does not fade a row whose key has already entered", () => {
    rememberTranscriptEntry("s1", "tc-9");
    const el = document.createElement("div");
    bind(el, { sessionId: "s1", entryKey: "tc-9" });
    expect(el.classList.contains(FADE_CLASS)).toBe(false);
  });

  it("does not replay when the same key is bound again (virtualized remount)", () => {
    const first = document.createElement("div");
    const second = document.createElement("div");
    bind(first, { sessionId: "s1", entryKey: "tc-remount" });
    bind(second, { sessionId: "s1", entryKey: "tc-remount" });
    expect(first.classList.contains(FADE_CLASS)).toBe(true);
    expect(second.classList.contains(FADE_CLASS)).toBe(false);
  });

  it("fades when only the stable row id is fresh even if wire tool_call_id was baselined", () => {
    rememberTranscriptEntry("s1", "call_1");
    const el = document.createElement("div");
    bind(el, { sessionId: "s1", entryKey: "a2:call_1" });
    expect(el.classList.contains(FADE_CLASS)).toBe(true);
  });

  it("fades a activity_span even when the lead chicklet already entered", () => {
    rememberTranscriptEntry("s1", "r1");
    const el = document.createElement("div");
    bind(el, { sessionId: "s1", entryKey: activitySpanEnterFadeKey("r1") });
    expect(el.classList.contains(FADE_CLASS)).toBe(true);
    expect(hasTranscriptEntryMemory("s1", activitySpanEnterFadeKey("r1"))).toBe(
      true,
    );
  });

  it("fades keyless rows without recording memory", () => {
    const el = document.createElement("div");
    bind(el, {});
    expect(el.classList.contains(FADE_CLASS)).toBe(true);
  });

  it("retires the class when the keyframe ends so a re-insert cannot replay", () => {
    const el = document.createElement("div");
    document.body.append(el);
    bind(el, { sessionId: "s1", entryKey: "u-retire" });
    expect(el.classList.contains(FADE_CLASS)).toBe(true);

    el.dispatchEvent(
      Object.assign(new Event("animationend"), {
        animationName: TRANSCRIPT_ENTER_FADE_ANIMATION,
      }),
    );
    // Removing the class prevents a settled row from repainting opacity zero.
    expect(el.classList.contains(FADE_CLASS)).toBe(false);
    el.remove();
  });

  it("binds the row that lands in a recycled node, not just the first one", async () => {
    // Recycled nodes use their current row key.
    const el = document.createElement("div");
    const [key, setKey] = createSignal("row-a");
    bindReactive(el, () => ({ sessionId: "s1", entryKey: key() }));
    expect(el.classList.contains(FADE_CLASS)).toBe(true);
    expect(hasTranscriptEntryMemory("s1", "row-a")).toBe(true);

    setKey("row-b");
    await Promise.resolve();
    expect(hasTranscriptEntryMemory("s1", "row-b")).toBe(true);
  });

  it("leaves a running fade alone when the node is recycled mid-animation", async () => {
    // Keep one fade registration per recycled node.
    const el = document.createElement("div");
    const [key, setKey] = createSignal("mid-a");
    bindReactive(el, () => ({ sessionId: "s1", entryKey: key() }));
    expect(el.classList.contains(FADE_CLASS)).toBe(true);

    setKey("mid-b");
    await Promise.resolve();
    expect(el.classList.contains(FADE_CLASS)).toBe(true);
    // One listener, so one animationend retires it.
    el.dispatchEvent(
      Object.assign(new Event("animationend"), {
        animationName: TRANSCRIPT_ENTER_FADE_ANIMATION,
      }),
    );
    expect(el.classList.contains(FADE_CLASS)).toBe(false);
    // Recorded regardless of whether it got its own fade.
    expect(hasTranscriptEntryMemory("s1", "mid-b")).toBe(true);
  });

  it("does not fade a recycled node whose new row is already history", async () => {
    const el = document.createElement("div");
    const [key, setKey] = createSignal("fresh-1");
    rememberTranscriptEntry("s1", "history-1");
    bindReactive(el, () => ({ sessionId: "s1", entryKey: key() }));
    el.dispatchEvent(
      Object.assign(new Event("animationend"), {
        animationName: TRANSCRIPT_ENTER_FADE_ANIMATION,
      }),
    );
    expect(el.classList.contains(FADE_CLASS)).toBe(false);

    setKey("history-1");
    await Promise.resolve();
    expect(el.classList.contains(FADE_CLASS)).toBe(false);
  });

  it("retires the class on the safety timeout when no keyframe runs", () => {
    vi.useFakeTimers();
    const el = document.createElement("div");
    bind(el, { sessionId: "s1", entryKey: "u-reduced-motion" });
    expect(el.classList.contains(FADE_CLASS)).toBe(true);

    // Reduced motion may omit the animation event.
    vi.advanceTimersByTime(TRANSCRIPT_ENTER_FADE_MS + 80);
    expect(el.classList.contains(FADE_CLASS)).toBe(false);
    vi.useRealTimers();
  });
});
