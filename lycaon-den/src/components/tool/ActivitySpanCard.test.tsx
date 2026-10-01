import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import { createSignal, type ComponentProps } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import { ActivitySpanCard as ActivitySpanCardView } from "./ActivitySpanCard.tsx";
import { ToolPartCard } from "./ToolPartCard.tsx";
import { TranscriptDiffGroup } from "../transcript/TranscriptDiffGroup.tsx";
import { fileEditStepsFromParts, foldFileEdits } from "../../chat/file-edit/file-edit-fold.ts";
import {
  createTranscriptDisclosureStore,
  TranscriptDisclosureProvider,
} from "../../chat/transcript/presentation/disclosure-state.tsx";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import type { ActivitySpanEntry } from "../../chat/transcript/projection/transcript-item-model.ts";
import {
  clearTranscriptEntryMemory,
  hasTranscriptEntryMemory,
  rememberTranscriptEntry,
  activitySpanEnterFadeKey,
} from "../../chat/transcript/presentation/transcript-entry.ts";

function ActivitySpanCard(
  props: Omit<ComponentProps<typeof ActivitySpanCardView>, "renderToolEntry">,
) {
  return (
    <ActivitySpanCardView
      {...props}
      renderToolEntry={(part) => (
        <ToolPartCard
          part={part()}
          layout={props.layout}
          sessionId={props.sessionId}
        />
      )}
    />
  );
}

const parts: ToolPartView[] = [
  {
    id: "r1",
    toolCallId: "r1",
    assistantMessageId: "assistant-message",
    messageId: "m1",
    tool: "read",
    kind: "read",
    status: "completed",
    title: "main.go",
    args: { path: "main.go" },
    output: "package main",
    error: null,
  },
  {
    id: "r2",
    toolCallId: "r2",
    assistantMessageId: "assistant-message",
    messageId: "m1",
    tool: "read",
    kind: "read",
    status: "completed",
    title: "handler.go",
    args: { path: "handler.go" },
    output: "package handler",
    error: null,
  },
];

const entries: ActivitySpanEntry[] = parts.map((part) => ({
  kind: "tool",
  part,
}));

afterEach(() => {
  clearTranscriptEntryMemory();
});

describe("ActivitySpanCard live layout", () => {
  it("keeps composition and output links mounted when entries are rebuilt", () => {
    const [current, setCurrent] = createSignal(entries);
    const { container } = render(() => (
      <ActivitySpanCard label="read" entries={current()} layout="chat" />
    ));
    const composition = container.querySelector(".den-activity-span-cmd");
    fireEvent.click(container.querySelector(".den-activity-span > summary")!);
    fireEvent.click(container.querySelector(".den-tool-part > summary")!);
    const output = container.querySelector('[data-testid="tool-content-link"]');
    expect(composition).not.toBeNull();
    expect(output).not.toBeNull();
    setCurrent(parts.map((part) => ({ kind: "tool", part: { ...part } })));
    expect(container.querySelector(".den-activity-span-cmd")).toBe(composition);
    expect(container.querySelector('[data-testid="tool-content-link"]')).toBe(output);
    setCurrent([{ kind: "tool", part: { ...parts[0]! } }]);
    expect(container.querySelector(".den-activity-span-cmd")).toBe(composition);
    expect(composition?.querySelector(".den-activity-span-cmd-count")).toBeNull();
  });
});

describe("ActivitySpanCard status and failures", () => {
  const withStatus = (
    part: ToolPartView,
    status: ToolPartView["status"],
  ): ActivitySpanEntry => ({ kind: "tool", part: { ...part, status } });

  const dotStatus = (span: ActivitySpanEntry[]): string | null => {
    const { container } = render(() => (
      <ActivitySpanCard label="read" entries={span} layout="chat" />
    ));
    return container
      .querySelector('[data-testid="activity-span-card"]')
      ?.getAttribute("data-status") ?? null;
  };

  it("takes the dot from the newest settled row", () => {
    expect(
      dotStatus([withStatus(parts[0]!, "error"), withStatus(parts[1]!, "completed")]),
    ).toBe("done");
    expect(
      dotStatus([withStatus(parts[0]!, "completed"), withStatus(parts[1]!, "error")]),
    ).toBe("error");
    expect(
      dotStatus([withStatus(parts[0]!, "error"), withStatus(parts[1]!, "running")]),
    ).toBe("running");
  });

  it("keeps a recovered failure on the collapsed summary", () => {
    const { container } = render(() => (
      <ActivitySpanCard
        label="read"
        entries={[
          withStatus(parts[0]!, "error"),
          withStatus(parts[1]!, "completed"),
        ]}
        layout="chat"
      />
    ));
    const span = container.querySelector('[data-testid="activity-span-card"]');
    expect(span?.getAttribute("data-status")).toBe("done");
    expect(
      container.querySelector('[data-testid="activity-span-failed"]')?.textContent,
    ).toBe("1 failed");
    expect(span?.querySelector("summary")?.getAttribute("aria-label")).toBe(
      "Read, done, 2 actions, 1 failed",
    );
  });

  it("omits the failure chip when nothing failed", () => {
    const { container } = render(() => (
      <ActivitySpanCard label="read" entries={entries} layout="chat" />
    ));
    expect(
      container.querySelector('[data-testid="activity-span-failed"]'),
    ).toBeNull();
  });
});

describe("ActivitySpanCard", () => {
  it("renders collapsed span chip with count", () => {
    const { container } = render(() => (
      <ActivitySpanCard
        label="read"
        entries={entries}
        layout="chat"
      />
    ));
    const span = container.querySelector('[data-testid="activity-span-card"]');
    expect(span).toBeTruthy();
    expect(span?.getAttribute("data-count")).toBe("2");
    expect(span?.textContent).toMatch(/Read/);
    expect(span?.textContent).toMatch(/×2/);
    expect(span?.querySelector(".den-activity-span-hint")?.textContent).toBe(
      "main.go, +1",
    );
    expect(container.querySelector(".den-activity-span-composition")).toBeTruthy();
  });

  it("uses the activity composition when no distinct detail is available", () => {
    const repeated: ActivitySpanEntry[] = parts.map((part) => ({
      kind: "tool",
      part: { ...part, title: "read", args: { path: "read" } },
    }));
    const { container } = render(() => (
      <ActivitySpanCard label="read" entries={repeated} layout="chat" />
    ));

    expect(
      container.querySelector(".den-activity-span-hint")?.textContent,
    ).toBe("Read ×2");
  });

  it("renders a singleton through the same span without a redundant count", () => {
    const { container } = render(() => (
      <ActivitySpanCard
        label="investigating"
        entries={[entries[0]!]}
        layout="chat"
      />
    ));
    const span = container.querySelector('[data-testid="activity-span-card"]');
    expect(span?.getAttribute("data-count")).toBe("1");
    expect(span?.querySelector("summary")?.getAttribute("aria-label")).toBe(
      "Investigating, done, 1 action",
    );
    expect(span?.querySelector(".den-activity-span-count")).toBeNull();
  });

  it("opens the sole tool use when its activity card is opened", async () => {
    const { container } = render(() => (
      <ActivitySpanCard
        label="investigating"
        entries={[entries[0]!]}
        layout="chat"
      />
    ));
    const span = container.querySelector(
      '[data-testid="activity-span-card"]',
    ) as HTMLDetailsElement;
    const tool = container.querySelector(
      ".den-activity-span-items .den-tool-part",
    ) as HTMLDetailsElement;

    fireEvent.click(span.querySelector(":scope > summary")!);
    expect(span.open).toBe(true);
    await waitFor(() => expect(tool.open).toBe(true));
  });

  it("renders an activity card for mixed investigation", () => {
    const mixed: ActivitySpanEntry[] = [
      ...entries,
      {
        kind: "tool",
        part: {
          id: "g1",
          toolCallId: "g1",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "grep",
          kind: "generic",
          status: "completed",
          title: "TODO",
          args: { pattern: "TODO", path: "." },
          output: "ok",
          error: null,
        },
      },
      {
        kind: "tool",
        part: {
          id: "g2",
          toolCallId: "g2",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "grep",
          kind: "generic",
          status: "completed",
          title: "FIXME",
          args: { pattern: "FIXME", path: "." },
          output: "ok",
          error: null,
        },
      },
    ];
    const { container } = render(() => (
      <ActivitySpanCard
        label="investigating"
        entries={mixed}
        layout="chat"
      />
    ));
    const span = container.querySelector('[data-testid="activity-span-card"]');
    expect(span?.getAttribute("data-count")).toBe("4");
    const chips = [...container.querySelectorAll(".den-activity-span-cmd")].map(
      (c) => ({
        name: c.querySelector(".den-activity-span-cmd-name")?.textContent,
        count: c.querySelector(".den-activity-span-cmd-count")?.textContent,
      }),
    );
    expect(chips).toEqual([
      { name: "Read", count: "×2" },
      { name: "Grep", count: "×2" },
    ]);
  });

  it("breaks down a mixed terminal span too", () => {
    const terminal: ActivitySpanEntry[] = [
      {
        kind: "tool",
        part: {
          id: "o1",
          toolCallId: "o1",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "terminal_open",
          kind: "generic",
          status: "completed",
          title: "command",
          args: { command: "command" },
          output: "ok",
          error: null,
        },
      },
      {
        kind: "tool",
        part: {
          id: "s1",
          toolCallId: "s1",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "terminal_send",
          kind: "generic",
          status: "completed",
          title: "ls",
          args: { id: "pty-1", data: "ls\n" },
          output: "ok",
          error: null,
        },
      },
    ];
    const { container } = render(() => (
      <ActivitySpanCard
        label="terminal"
        entries={terminal}
        layout="chat"
      />
    ));
    expect(
      [...container.querySelectorAll(".den-activity-span-cmd-name")].map(
        (el) => el.textContent,
      ),
    ).toEqual(["Terminal open", "Terminal send"]);
  });

  it("keeps span arrival state independent from nested rows", () => {
    rememberTranscriptEntry("s1", "r1");
    const { container } = render(() => (
      <ActivitySpanCard
        label="terminal"
        entries={entries}
        layout="chat"
        sessionId="s1"
        entryKey="r1"
      />
    ));
    const span = container.querySelector('[data-testid="activity-span-card"]');
    expect(span?.classList.contains("den-enter-fade")).toBe(true);
    expect(
      hasTranscriptEntryMemory("s1", activitySpanEnterFadeKey("r1")),
    ).toBe(true);
  });

  it("fades in on first appearance", () => {
    const { container } = render(() => (
      <ActivitySpanCard
        label="terminal"
        entries={entries}
        layout="chat"
        sessionId="s1"
        entryKey="r1"
      />
    ));
    const span = container.querySelector('[data-testid="activity-span-card"]');
    expect(span?.classList.contains("den-enter-fade")).toBe(true);
  });

  it("expanding the span does not expand the first child card", () => {
    // Span and row disclosures use separate keys.
    const { container } = render(() => (
      <ActivitySpanCard
        label="read"
        entries={entries}
        layout="chat"
        sessionId="s1"
        entryKey="r1"
      />
    ));
    const span = container.querySelector(
      '[data-testid="activity-span-card"]',
    ) as HTMLDetailsElement;
    fireEvent.click(span.querySelector(":scope > summary")!);
    expect(span.open).toBe(true);
    const firstChild = container.querySelector(
      '.den-activity-span-items .den-tool-part',
    ) as HTMLDetailsElement;
    expect(firstChild.open).toBe(false);
  });

  it("keeps nested tool rows mounted across streaming entry patches", () => {
    const [streamEntries, setStreamEntries] = createSignal<ActivitySpanEntry[]>([
      {
        kind: "tool",
        part: {
          id: "r1",
          toolCallId: "r1",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "read",
          kind: "read",
          status: "running",
          title: "main.go",
          args: { path: "main.go" },
          output: "",
          error: null,
        },
      },
      {
        kind: "tool",
        part: {
          id: "r2",
          toolCallId: "r2",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "read",
          kind: "read",
          status: "running",
          title: "handler.go",
          args: { path: "handler.go" },
          output: "",
          error: null,
        },
      },
    ]);
    const { container } = render(() => (
      <ActivitySpanCard
        label="read"
        entries={streamEntries()}
        layout="chat"
        sessionId="s1"
        entryKey="r1"
      />
    ));
    const before = container.querySelector(
      '.den-activity-span-items .den-tool-part',
    ) as HTMLElement;
    expect(before).toBeTruthy();
    expect(before.getAttribute("data-status")).toBe("running");

    setStreamEntries([
      {
        kind: "tool",
        part: {
          id: "r1",
          toolCallId: "r1",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "read",
          kind: "read",
          status: "completed",
          title: "main.go",
          args: { path: "main.go" },
          output: "package main",
          error: null,
        },
      },
      {
        kind: "tool",
        part: {
          id: "r2",
          toolCallId: "r2",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "read",
          kind: "read",
          status: "running",
          title: "handler.go",
          args: { path: "handler.go" },
          output: "",
          error: null,
        },
      },
    ]);

    const after = container.querySelector(
      '.den-activity-span-items .den-tool-part',
    ) as HTMLElement;
    // Stable DOM identity preserves disclosure and fade state.
    expect(after).toBe(before);
    expect(after.getAttribute("data-status")).toBe("done");
  });

  it("keeps meta-span rows mounted across entry patches", () => {
    const [mixed, setMixed] = createSignal<ActivitySpanEntry[]>([
      ...entries.map((entry) =>
        entry.kind === "tool"
          ? {
              kind: "tool" as const,
              part: { ...entry.part, status: "running" as const, output: "" },
            }
          : entry,
      ),
      {
        kind: "tool",
        part: {
          id: "g1",
          toolCallId: "g1",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "grep",
          kind: "generic",
          status: "running",
          title: "TODO",
          args: { pattern: "TODO", path: "." },
          output: "",
          error: null,
        },
      },
      {
        kind: "tool",
        part: {
          id: "g2",
          toolCallId: "g2",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "grep",
          kind: "generic",
          status: "running",
          title: "FIXME",
          args: { pattern: "FIXME", path: "." },
          output: "",
          error: null,
        },
      },
    ]);
    const { container } = render(() => (
      <ActivitySpanCard
        label="investigating"
        entries={mixed()}
        layout="chat"
        sessionId="s1"
        entryKey="r1"
      />
    ));
    const before = container.querySelector(
      ".den-activity-span-items .den-tool-part",
    ) as HTMLElement;
    expect(before).toBeTruthy();
    expect(before.getAttribute("data-status")).toBe("running");
    const fading = before.classList.contains("den-enter-fade");

    setMixed(
      mixed().map((entry) => {
        if (entry.kind !== "tool") return entry;
        return {
          kind: "tool",
          part: {
            ...entry.part,
            status: "completed",
            output: entry.part.output || "ok",
          },
        };
      }),
    );

    const after = container.querySelector(
      ".den-activity-span-items .den-tool-part",
    ) as HTMLElement;
    expect(after).toBe(before);
    expect(after.classList.contains("den-enter-fade")).toBe(fading);
    expect(after.getAttribute("data-status")).toBe("done");
  });

  it("renders index warming activity inside a web research span", () => {
    const webEntries: ActivitySpanEntry[] = [
      {
        kind: "tool",
        part: {
          id: "ws1",
          toolCallId: "ws1",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "web_search",
          kind: "generic",
          status: "completed",
          title: "solid virtualizer",
          args: { query: "solid virtualizer" },
          output: "ok",
          error: null,
        },
      },
      {
        kind: "index_warming",
        key: "iw1",
        meta: { trigger: "declared_url", tier: "crawl", topic: "docs.example", pages: 3 },
      },
    ];
    const { container } = render(() => (
      <ActivitySpanCard
        label="web research"
        entries={webEntries}
        layout="chat"
      />
    ));
    const span = container.querySelector('[data-testid="activity-span-card"]');
    expect(span?.getAttribute("data-tool")).toBe("web research");
    expect(span?.getAttribute("data-count")).toBe("2");
    span?.setAttribute("open", "open");
    expect(
      container.querySelector('[data-testid="index-warming-chicklet"]'),
    ).toBeTruthy();
  });

  it("uses chip layout for nested tools in any transcript layout", () => {
    for (const layout of ["chat", "worker"] as const) {
      const { container } = render(() => (
        <ActivitySpanCard
          label="read"
          entries={entries}
          layout={layout}
        />
      ));
      const span = container.querySelector(".den-activity-span");
      span?.setAttribute("open", "open");
      expect(
        container.querySelector(`.den-tool-part[data-layout="${layout}"]`),
      ).toBeTruthy();
    }
  });

  it("marks activity-span rows with plus/minus and keeps the caret on the span header", () => {
    const { container } = render(() => (
      <ActivitySpanCard
        label="read"
        entries={entries}
        layout="chat"
      />
    ));
    const caret = container.querySelector(
      ".den-activity-span > summary .den-tool-chicklet-caret",
    );
    expect(caret).toBeTruthy();
    expect(caret?.parentElement?.className).toBe("den-activity-span-chicklet");
    const rows = container.querySelectorAll(".den-activity-span-items .den-tool-part");
    expect(rows).toHaveLength(2);
    for (const row of rows) {
      expect(row.querySelector(".den-row-mark")).toBeTruthy();
      expect(row.querySelector(".den-tool-chicklet-caret")).toBeNull();
    }
  });

  it("opens one row at a time — a second row closes the first", () => {
    const { container } = render(() => (
      <ActivitySpanCard
        label="read"
        entries={entries}
        layout="chat"
        sessionId="s1"
        entryKey="r1"
      />
    ));
    const rows = [
      ...container.querySelectorAll(".den-activity-span-items .den-tool-part"),
    ] as HTMLDetailsElement[];
    expect(rows).toHaveLength(2);
    expect(rows.filter((r) => r.open)).toHaveLength(0);

    fireEvent.click(rows[0]!.querySelector("summary")!);
    expect(rows[0]!.open).toBe(true);
    expect(rows[1]!.open).toBe(false);

    fireEvent.click(rows[1]!.querySelector("summary")!);
    expect(rows[0]!.open).toBe(false);
    expect(rows[1]!.open).toBe(true);

    fireEvent.click(rows[1]!.querySelector("summary")!);
    expect(rows.filter((r) => r.open)).toHaveLength(0);
  });

  it("renders a mixed investigating span as flat rows", () => {
    const mixed: ActivitySpanEntry[] = [
      ...entries,
      {
        kind: "tool",
        part: {
          id: "g1",
          toolCallId: "g1",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "grep",
          kind: "generic",
          status: "completed",
          title: "TODO",
          args: { pattern: "TODO", path: "." },
          output: "ok",
          error: null,
        },
      },
      {
        kind: "tool",
        part: {
          id: "g2",
          toolCallId: "g2",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "grep",
          kind: "generic",
          status: "completed",
          title: "FIXME",
          args: { pattern: "FIXME", path: "." },
          output: "ok",
          error: null,
        },
      },
    ];
    const { container } = render(() => (
      <ActivitySpanCard
        label="investigating"
        entries={mixed}
        layout="chat"
      />
    ));
    const span = container.querySelector(
      '[data-testid="activity-span-card"]',
    ) as HTMLElement;
    expect(span.getAttribute("data-count")).toBe("4");
    expect(container.querySelectorAll(".den-activity-span-cmd")).toHaveLength(2);
    span.setAttribute("open", "open");

    const rows = [
      ...container.querySelectorAll(".den-activity-span-items .den-tool-part"),
    ];
    expect(rows.map((r) => r.getAttribute("data-tool"))).toEqual([
      "read",
      "read",
      "grep",
      "grep",
    ]);
    for (const row of rows) {
      expect(row.parentElement?.className).toBe("den-activity-span-item");
    }
  });

  it("renders a mixed editing span as flat rows", () => {
    const mixed: ActivitySpanEntry[] = [
      {
        kind: "tool",
        part: {
          id: "w1",
          toolCallId: "w1",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "write",
          kind: "write",
          status: "completed",
          title: "a.go",
          args: { path: "a.go" },
          output: "ok",
          error: null,
          fileEdit: fileEditPreviewFixture({ path: "a.go", before: "", after: "a" }),
        },
      },
      {
        kind: "tool",
        part: {
          id: "e1",
          toolCallId: "e1",
          assistantMessageId: "assistant-message",
          messageId: "m1",
          tool: "edit",
          kind: "write",
          status: "completed",
          title: "c.go",
          args: { path: "c.go" },
          output: "ok",
          error: null,
          fileEdit: fileEditPreviewFixture({ path: "c.go", before: "x", after: "y" }),
        },
      },
    ];
    const { container } = render(() => (
      <ActivitySpanCard
        label="editing"
        entries={mixed}
        layout="chat"
      />
    ));
    const span = container.querySelector(
      '[data-testid="activity-span-card"]',
    ) as HTMLElement;
    expect(span.getAttribute("data-tool")).toBe("editing");
    span.setAttribute("open", "open");

    expect(
      [...container.querySelectorAll(".den-activity-span-items .den-tool-part")].map(
        (r) => r.getAttribute("data-tool"),
      ),
    ).toEqual(["write", "edit"]);
  });

  it("opens a write row while the turn's diff group shows the same file", async () => {
    const writes: ToolPartView[] = ["package.json", "src/db.js"].map((path, i) => ({
      id: `a1:w${i}`,
      toolCallId: `w${i}`,
      assistantMessageId: "a1",
      messageId: "m1",
      tool: "write",
      kind: "write",
      status: "completed",
      title: path,
      args: { path, content: "{}" },
      output: "ok",
      error: null,
      fileEdit: fileEditPreviewFixture({ path, before: null, after: "{}" }),
    }));
    const store = createTranscriptDisclosureStore();
    const { container } = render(() => (
      <TranscriptDisclosureProvider value={store}>
        <ActivitySpanCard
          label="making changes"
          entries={writes.map((part) => ({ kind: "tool", part }))}
          layout="chat"
          sessionId="s1"
          entryKey="span-w"
        />
        <TranscriptDiffGroup
          folds={foldFileEdits(fileEditStepsFromParts(writes))}
          layout="chat"
          sessionId="s1"
          entryKey="turn-diffs"
        />
      </TranscriptDisclosureProvider>
    ));
    fireEvent.click(container.querySelector(".den-activity-span > summary")!);
    fireEvent.click(container.querySelector(".den-diff-group > summary")!);
    const row = container.querySelector('.den-tool-part[data-tool-call-id="w0"]') as HTMLDetailsElement;
    fireEvent.click(row.querySelector("summary")!);
    await waitFor(() => expect(row.open).toBe(true));
    expect(store.isOpen(transcriptDisclosureKey.tool(writes[0]!.id))).toBe(true);
  });

  it("renders 16 entries in natural flow without virtualizing or expanding beyond contained changes", () => {
    const sixteenEntries: ActivitySpanEntry[] = Array.from({ length: 16 }, (_, i) => ({
      kind: "tool",
      part: {
        id: `tool-${i}`,
        toolCallId: `call-${i}`,
        assistantMessageId: "assistant-1",
        messageId: `msg-${i}`,
        tool: i % 2 === 0 ? "read" : "grep",
        kind: i % 2 === 0 ? "read" : "generic",
        status: "completed",
        title: `item-${i}.txt`,
        args: { path: `item-${i}.txt` },
        output: "ok",
        error: null,
      },
    }));

    const { container } = render(() => (
      <ActivitySpanCard
        label="investigating"
        entries={sixteenEntries}
        layout="chat"
      />
    ));

    const span = container.querySelector(
      '[data-testid="activity-span-card"]',
    ) as HTMLDetailsElement;
    expect(span.getAttribute("data-count")).toBe("16");
    span.setAttribute("open", "open");

    const renderedParts = container.querySelectorAll(
      ".den-activity-span-items .den-tool-part",
    );
    expect(renderedParts).toHaveLength(16);
    expect(container.querySelector('[data-testid="virtual-card-list"]')).toBeNull();
  });
});
