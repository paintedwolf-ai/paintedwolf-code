// @vitest-environment jsdom
import { createRoot, createSignal } from "solid-js";
import { describe, expect, it } from "vitest";
import type { TranscriptItem } from "./transcript-item-model.ts";
import {
  EMPTY_TRANSCRIPT_SPAN_STATE,
  bindTranscriptSpanAnchors,
  reconcileRegroupedActivitySpans,
  resolveTranscriptItemAnchor,
  transcriptItemContainsMessage,
  transcriptToolTarget,
  visibleTranscriptToolTarget,
} from "./transcript-item-anchors.ts";

describe("visibleTranscriptToolTarget", () => {
  it("follows nested disclosures down to the selected command as they open", () => {
    const root = document.createElement("div");
    root.innerHTML = '<div id="outer" data-transcript-disclosure><button>Activity</button><div id="inner" data-transcript-disclosure><button>Task</button><div class="den-tool-part" data-transcript-disclosure data-tool-call-id="chosen"><button>Command</button></div></div></div>';
    const outer = root.querySelector<HTMLElement>("#outer")!;
    const inner = root.querySelector<HTMLElement>("#inner")!;
    const command = root.querySelector<HTMLElement>("[data-tool-call-id]")!;
    expect(visibleTranscriptToolTarget(root, "chosen")).toBe(outer);
    outer.setAttribute("data-expanded", String(true));
    expect(visibleTranscriptToolTarget(root, "chosen")).toBe(inner);
    inner.setAttribute("data-expanded", String(true));
    expect(visibleTranscriptToolTarget(root, "chosen")).toBe(command);
    outer.setAttribute("data-expanded", String(false));
    expect(visibleTranscriptToolTarget(root, "chosen")).toBe(outer);
  });

  it("keeps the containing card selected while its body is animating", () => {
    const root = document.createElement("div");
    root.innerHTML = '<div id="card" data-transcript-disclosure data-expanded="true" data-animating="true"><button>Activity</button><div class="den-tool-part" data-tool-call-id="chosen"></div></div>';
    expect(visibleTranscriptToolTarget(root, "chosen")).toBe(root.querySelector("#card"));
    root.querySelector("#card")!.removeAttribute("data-animating");
    expect(visibleTranscriptToolTarget(root, "chosen")).toBe(root.querySelector("[data-tool-call-id]"));
  });
});

describe("transcriptToolTarget", () => {
  it("finds a folded diff through its row's tool-call identities", () => {
    const root = document.createElement("div");
    root.innerHTML = '<div data-tool-call-ids="first second"><div class="den-diff-group den-transcript-disclosure-card" data-transcript-disclosure><button>1 file changed</button></div></div>';
    expect(transcriptToolTarget(root, "second")).toBe(root.querySelector(".den-diff-group"));
    expect(transcriptToolTarget(root, "sec")).toBeNull();
  });
  it("returns the visible card around a nested tool marker", () => {
    const root = document.createElement("div");
    root.innerHTML = `
      <article data-testid="task-card">
        <div data-tool-call-id="t1"></div>
      </article>
    `;

    expect(transcriptToolTarget(root, "t1")).toBe(
      root.querySelector('[data-testid="task-card"]'),
    );
  });

  it("matches tool IDs without building a selector from them", () => {
    const root = document.createElement("div");
    const card = document.createElement("div");
    card.className = "den-tool-part";
    card.dataset.toolCallId = 'call"1';
    root.append(card);

    expect(transcriptToolTarget(root, 'call"1')).toBe(card);
  });
});

it("a pending seat becomes a navigation target only after host confirmation", () => {
  const pending = { kind: "prompt" as const, operationId: "op", text: "hello", state: "sending" as const, createdAt: 1 };
  expect(transcriptItemContainsMessage({ kind: "pending_user", key: "op", text: "hello", pending }, "op")).toBe(false);
  expect(transcriptItemContainsMessage({ kind: "user", key: "op", text: "hello" }, "op")).toBe(true);
});

describe("regrouped activity span anchors", () => {
  const spanItem = (key: string, entryKeys: string[]): TranscriptItem => ({
    kind: "activity_span",
    key,
    label: "reading",
    entries: entryKeys.map((entry) => ({
      kind: "index_warming",
      key: entry,
      meta: {} as never,
    })),
  });

  it("transfers span disclosure state across growth, shrinkage, and two-span merges", () => {
    const renames: Array<[string, string]> = [];
    const initial = reconcileRegroupedActivitySpans(
      [spanItem("call-b", ["call-b", "call-c"])],
      EMPTY_TRANSCRIPT_SPAN_STATE,
      (from, to) => renames.push([from, to]),
    );
    expect([...initial.spanEntries.keys()]).toEqual(["call-b"]);
    expect(renames).toEqual([]);

    // Growth when an older page prepends call-a.
    const grown = reconcileRegroupedActivitySpans(
      [spanItem("call-a", ["call-a", "call-b", "call-c"])],
      initial,
      (from, to) => renames.push([from, to]),
    );
    expect([...grown.spanEntries.keys()]).toEqual(["call-a"]);
    expect(renames).toEqual([["call-b", "call-a"]]);

    // Shrinkage when the older page evicts call-a.
    renames.length = 0;
    const shrunk = reconcileRegroupedActivitySpans(
      [spanItem("call-b", ["call-b", "call-c"]), spanItem("call-e", ["call-e", "call-f"])],
      grown,
      (from, to) => renames.push([from, to]),
    );
    expect(renames).toEqual([["call-a", "call-b"]]);

    // Merging two existing spans when a gap page supplies call-d between them.
    renames.length = 0;
    reconcileRegroupedActivitySpans(
      [spanItem("call-b", ["call-b", "call-c", "call-d", "call-e", "call-f"])],
      shrunk,
      (from, to) => renames.push([from, to]),
    );
    expect(renames).toEqual([["call-e", "call-b"]]);
  });

  it("resolves vanished span keys for growth, shrinkage, and DOM-measured open entries", () => {
    const previous = reconcileRegroupedActivitySpans(
      [spanItem("call-a", ["call-a", "call-b", "call-c"])],
      EMPTY_TRANSCRIPT_SPAN_STATE,
      () => {},
    );
    const grownItems: TranscriptItem[] = [
      { kind: "user", key: "u-1", text: "prompt" },
      spanItem("call-a", ["call-a", "call-b", "call-c"]),
    ];
    expect(
      resolveTranscriptItemAnchor(grownItems, "call-b", () => false, 42),
    ).toMatchObject({ rowKey: "call-a", offsetPx: 0 });
    expect(
      resolveTranscriptItemAnchor(grownItems, "call-c", () => true, 42),
    ).toMatchObject({ rowKey: "call-a", offsetPx: 84 });

    // Measured DOM entry offsets override the fallback per-entry estimate when mounted.
    const root = document.createElement("div");
    root.innerHTML = `
      <details class="den-activity-span" open>
        <summary>Reading</summary>
        <div class="den-activity-span-item" data-activity-entry-key="call-a"></div>
        <div class="den-activity-span-item" data-activity-entry-key="call-b"></div>
        <div class="den-activity-span-item" data-activity-entry-key="call-c"></div>
      </details>
    `;
    const entries = root.querySelectorAll<HTMLElement>("[data-activity-entry-key]");
    Object.defineProperty(entries[0]!, "offsetTop", { value: 40, configurable: true });
    Object.defineProperty(entries[2]!, "offsetTop", { value: 176, configurable: true });
    const previousSpanLead = reconcileRegroupedActivitySpans(
      [spanItem("call-c", ["call-c"])],
      EMPTY_TRANSCRIPT_SPAN_STATE,
      () => {},
    );
    expect(
      resolveTranscriptItemAnchor(grownItems, "call-c", () => true, 42, {
        previous: previousSpanLead,
        root: () => root,
      }),
    ).toMatchObject({ rowKey: "call-a", offsetPx: 136 });

    // When the span was previously the first row, a newly acquired row seam is included.
    const rowRoot = document.createElement("div");
    rowRoot.innerHTML = `
      <div data-msg-id="call-a">
        <div class="den-activity-span" data-transcript-disclosure="" data-expanded="true">
          <div class="den-activity-span-item" data-activity-entry-key="call-a"></div>
          <div class="den-activity-span-item" data-activity-entry-key="call-c"></div>
        </div>
      </div>
    `;
    const rowEl = rowRoot.querySelector<HTMLElement>("[data-msg-id]")!;
    const spanEl = rowRoot.querySelector<HTMLElement>(".den-activity-span")!;
    const rowEntries = rowRoot.querySelectorAll<HTMLElement>("[data-activity-entry-key]");
    rowEl.getBoundingClientRect = () => ({ top: 100 } as DOMRect);
    spanEl.getBoundingClientRect = () => ({ top: 120 } as DOMRect);
    rowEntries[0]!.getBoundingClientRect = () => ({ top: 150 } as DOMRect);
    rowEntries[1]!.getBoundingClientRect = () => ({ top: 212 } as DOMRect);
    expect(
      resolveTranscriptItemAnchor(grownItems, "call-c", () => true, 42, {
        previous: previousSpanLead,
        root: () => rowRoot,
      }),
    ).toMatchObject({ rowKey: "call-a", offsetPx: 82 });

    // Shrinkage when call-a is evicted and call-b becomes the surviving span lead.
    const shrunkItems: TranscriptItem[] = [
      spanItem("call-b", ["call-b", "call-c"]),
    ];
    expect(
      resolveTranscriptItemAnchor(shrunkItems, "call-a", () => true, 42, { previous }),
    ).toEqual({ rowKey: "call-b", offsetPx: -42 });
    expect(
      resolveTranscriptItemAnchor(grownItems, "missing", () => true, 42),
    ).toBeUndefined();
  });

  it("synchronizes disclosure renames on demand when anchor resolution precedes reactive flush", () => {
    const openKeys = new Set<string>(["activity-span:call-b"]);
    let items: TranscriptItem[] = [spanItem("call-b", ["call-b", "call-c"])];
    const [tick, setTick] = createSignal(0);
    createRoot((dispose) => {
      const resolve = bindTranscriptSpanAnchors({
        items: () => {
          tick();
          return items;
        },
        isOpen: (key) => openKeys.has(key),
        rename: (from, to) => {
          if (openKeys.delete(from)) openKeys.add(to);
        },
      });
      items = [spanItem("call-a", ["call-a", "call-b", "call-c"])];
      expect(resolve("call-b")).toMatchObject({ rowKey: "call-a", offsetPx: 42 });
      expect(openKeys.has("activity-span:call-a")).toBe(true);
      setTick(1);
      dispose();
    });
  });

  it("transfers multi-section activity_group disclosures and diff-file keys across prepend, eviction, and merge", () => {
    const groupItem = (lead: string, entries: string[], paths: string[] = []): TranscriptItem => ({
      kind: "activity_group",
      key: `activity-group:${lead}`,
      sectionOrder: ["activities", "approvals", "files"],
      headline: "editing files",
      anchorMessageId: lead,
      approvals: [{ kind: "checkpoint", key: "checkpoint:cp-1", parentMessageId: lead, meta: { checkpoint_id: "cp-1", kind: "tool_approval", status: "approved" } }],
      folds: paths.map((path) => ({
        path,
        key: `activity-group:${lead}\0${path}`,
        net: { path, before_sha256: "a", after_sha256: "b", created: false, added: 1, removed: 0 },
        steps: [{ key: `${lead}:0`, messageId: lead, toolCallId: lead, tool: "write", snapshot: { path, before_sha256: "a", after_sha256: "b", created: false, added: 1, removed: 0 } }],
      })),
      entries: entries.map((entry) => ({ kind: "index_warming", key: entry, meta: {} as never })),
    });

    const openKeys = new Set<string>([
      "activity-group-approvals:activity-group:call-b",
      "activity-group-files:activity-group:call-b",
      "activity-group-activities:activity-group:call-b",
      "diff-file:activity-group:call-b\0src/a.go",
    ]);
    let items: TranscriptItem[] = [groupItem("call-b", ["call-b", "call-c"], ["src/a.go"])];
    const [tick, setTick] = createSignal(0);
    createRoot((dispose) => {
      const resolve = bindTranscriptSpanAnchors({
        items: () => {
          tick();
          return items;
        },
        isOpen: (key) => openKeys.has(key),
        rename: (from, to) => {
          if (openKeys.delete(from)) openKeys.add(to);
        },
      });
      setTick(1);

      items = [groupItem("call-a", ["call-a", "call-b", "call-c"], ["src/a.go"])];
      expect(resolve("activity-group:call-b")).toMatchObject({
        rowKey: "activity-group:call-a",
        offsetPx: 42,
      });
      expect([...openKeys].sort()).toEqual([
        "activity-group-activities:activity-group:call-a",
        "activity-group-approvals:activity-group:call-a",
        "activity-group-files:activity-group:call-a",
        "diff-file:activity-group:call-a\0src/a.go",
      ]);

      items = [groupItem("call-b", ["call-b", "call-c"], ["src/a.go"])];
      expect(resolve("activity-group:call-a")).toEqual({
        rowKey: "activity-group:call-b",
        offsetPx: -42,
      });
      expect(openKeys.has("diff-file:activity-group:call-b\0src/a.go")).toBe(true);
      dispose();
    });
  });

  it("prefers the path-matching command card and Activities disclosure shell in transcriptToolTarget", () => {
    const root = document.createElement("div");
    root.innerHTML = `
      <div data-msg-id="activity-group:c1" data-tool-call-ids="c1">
        <div data-activity-group-section="approvals">
          <div class="den-transcript-disclosure-card" data-transcript-disclosure="">Approvals</div>
        </div>
        <div data-activity-group-section="activities">
          <div id="act-shell" class="den-activity-span den-transcript-disclosure-card" data-transcript-disclosure="">Activities</div>
        </div>
      </div>
    `;
    expect(transcriptToolTarget(root, "c1")?.id).toBe("act-shell");

    const openRoot = document.createElement("div");
    openRoot.innerHTML = `
      <div class="den-tool-part" data-tool-call-id="cmd-348" data-activity-mutate-path="a.go">Write a.go</div>
      <div id="target-b" class="den-tool-part" data-tool-call-id="cmd-348" data-activity-mutate-path="pkg/b_test.go">Write b_test.go</div>
    `;
    expect(transcriptToolTarget(openRoot, "cmd-348", "pkg/b_test.go")?.id).toBe("target-b");
  });
});

