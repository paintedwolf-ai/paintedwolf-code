import { isDisclosureOpen } from "../test/transcript-disclosure-fixture.ts";
import { afterEach, describe, expect, it, onTestFinished, vi } from "vitest";
import { applyWebEngineFact } from "../platform/desktop/webkit-atspi-workaround.ts";
import { render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal, type JSX } from "solid-js";
import {
  closeFind,
  findController,
  findNext,
  openFind,
  resetFindControllerForTests,
  setFindQuery,
} from "./find-controller.ts";
import { FIND_MARK_ATTR } from "./find-match.ts";
import { bindFindableView } from "./use-findable-view.ts";
import { registerOpenFilesSurfaceSink, resetOpenFilesSurfaceForTests } from "../platform/navigation/open-files-surface.ts";
import { ToolPartShell } from "../components/tool/ToolPartShell.tsx";
import { ToolPartCard } from "../components/tool/ToolPartCard.tsx";
import { ActivitySpanCard } from "../components/tool/ActivitySpanCard.tsx";
import type { ToolPartView } from "../chat/tool/tool-part-model.ts";
import type { ActivitySpanEntry } from "../chat/transcript/projection/transcript-item-model.ts";
import { clearTranscriptEntryMemory } from "../chat/transcript/presentation/transcript-entry.ts";
import { setLycaonClientForTest } from "../platform/connection/app-connection.ts";
import { stubClient } from "../test/client-fixture.ts";

afterEach(() => {
  setLycaonClientForTest(null);
  resetFindControllerForTests();
  resetOpenFilesSurfaceForTests();
  clearTranscriptEntryMemory();
  document.body.replaceChildren();
});

const part = (): ToolPartView => ({
  id: "a1:tc-find-1",
  toolCallId: "tc-find-1",
  assistantMessageId: "assistant-message",
  messageId: "m1",
  tool: "read",
  kind: "read",
  status: "completed",
  args: { path: "main.go" },
  output: "TOOL_BODY_FIND_NEEDLE content",
  error: null,
});

function FindRoot(props: { children: JSX.Element }) {
  const [root, setRoot] = createSignal<HTMLElement | null>(null);
  bindFindableView({
    id: "session-transcript-test",
    root,
    primary: true,
  });
  return (
    <div ref={setRoot} data-testid="find-root">
      {props.children}
    </div>
  );
}

describe("tool body find + collapsed reveal", () => {
  it("matches collapsed body text and reports collapsed count without expanding", async () => {
    render(() => (
      <ToolPartShell part={part()} layout="chat" sessionId="s1">
        <p>TOOL_BODY_FIND_NEEDLE content</p>
      </ToolPartShell>
    ));

    const card = await screen.findByTestId("tool-part-card");
    expect(isDisclosureOpen(card)).toBe(false);
    card.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));

    openFind();
    setFindQuery("TOOL_BODY_FIND_NEEDLE");
    expect(findController.matches().length).toBeGreaterThanOrEqual(1);
    expect(findController.collapsedCount()).toBeGreaterThanOrEqual(1);
    expect(isDisclosureOpen(card)).toBe(false);
  });

  it("expands on first next, restores on close", async () => {
    render(() => (
      <ToolPartShell part={part()} layout="chat" sessionId="s1">
        <p>TOOL_BODY_FIND_NEEDLE content</p>
      </ToolPartShell>
    ));

    const card = (await screen.findByTestId(
      "tool-part-card",
    )) as HTMLElement;
    card.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
    openFind();
    setFindQuery("TOOL_BODY_FIND_NEEDLE");
    expect(findController.collapsedCount()).toBeGreaterThanOrEqual(1);

    findNext();
    expect(isDisclosureOpen(card)).toBe(true);
    expect(findController.collapsedCount()).toBe(0);
    expect(
      document.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`).length,
    ).toBeGreaterThanOrEqual(1);

    closeFind();
    expect(isDisclosureOpen(card)).toBe(false);
  });

  it.each([
    ["by default", false],
    ["under the WebKitGTK AT-SPI workaround", true],
  ])("drills a collapsed activity span and nested disclosure through temporary leases %s", async (_mode, workaround) => {
    if (workaround) {
      applyWebEngineFact({ engine: "webkitgtk", version: "2.50.4" });
      onTestFinished(() => applyWebEngineFact(null));
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
        args: { path: "handler.go" },
        output: "GROUP_NESTED_FIND_NEEDLE in body",
        error: null,
      },
    ];
    const entries: ActivitySpanEntry[] = parts.map((p) => ({
      kind: "tool",
      part: p,
    }));

    render(() => (
      <FindRoot>
        <ActivitySpanCard
          label="read"
          entries={entries}
          layout="chat"
          sessionId="s1"
          entryKey="r1"
          renderToolEntry={(toolPart) => (
            <ToolPartCard part={toolPart()} layout="chat" sessionId="s1" />
          )}
        />
      </FindRoot>
    ));

    const span = (await screen.findByTestId(
      "activity-span-card",
    )) as HTMLElement;
    const cards = () => screen.queryAllByTestId("action-card");
    expect(isDisclosureOpen(span)).toBe(false);
    expect(cards()).toHaveLength(0);
    expect(cards().every((c) => !isDisclosureOpen(c))).toBe(true);

    openFind();
    setFindQuery("GROUP_NESTED_FIND_NEEDLE");
    expect(findController.collapsedCount()).toBeGreaterThanOrEqual(1);
    expect(isDisclosureOpen(span)).toBe(false);

    findNext();
    expect(isDisclosureOpen(span)).toBe(true);
    const nested = cards().find(
      (c) => c.getAttribute("data-tool-call-id") === "r2",
    );
    expect(isDisclosureOpen(nested)).toBe(true);
    expect(findController.collapsedCount()).toBe(0);

    closeFind();
    expect(isDisclosureOpen(span)).toBe(false);
    expect(isDisclosureOpen(cards().find((c) => c.getAttribute("data-tool-call-id") === "r2"))).toBe(false);
  });

  it("opens linked inline output from a collapsed action card", async () => {
    const sink = vi.fn();
    registerOpenFilesSurfaceSink(sink);
    render(() => <FindRoot><ToolPartCard part={part()} layout="chat" sessionId="s1" projectId="project" /></FindRoot>);
    openFind();
    setFindQuery("TOOL_BODY_FIND_NEEDLE");
    expect(findController.collapsedCount()).toBeGreaterThan(0);
    findNext();
    await waitFor(() => expect(sink).toHaveBeenCalled());
    const request = sink.mock.lastCall?.[0];
    expect(request).toMatchObject({ kind: "chat-content", projectId: "project", document: {
      kind: "tool", sessionId: "s1", content: { kind: "inline", text: part().output },
    } });
  });

  it.each(["chat", "worker"] as const)(
    "reveals a folded member's retained output inside a closed span in %s layout and restores state",
    async (layout) => {
      const sink = vi.fn();
      registerOpenFilesSurfaceSink(sink);
      const retainedRef = {
        field: "tool_output" as const,
        tool_call_id: "member-call",
        sha256: "blob-member-2",
        size_bytes: 1024,
        total_runes: 512,
        rows: 20,
      };
      const searchChatContent = vi.fn(async () => ({
        matches: [{ offset: 42, length: 6, preview: "RETAINED_FOLD_NEEDLE" }],
      }));
      setLycaonClientForTest(stubClient({ searchChatContent }));

      const foldedLead: ToolPartView = {
        id: "lead-row",
        toolCallId: "lead-call",
        assistantMessageId: "a1",
        messageId: "m-lead",
        tool: "read",
        kind: "read",
        status: "completed",
        args: { path: "first.go" },
        output: "first body",
        error: null,
        foldMembers: [
          {
            id: "member-row",
            toolCallId: "member-call",
            assistantMessageId: "a2",
            messageId: "m-member",
            tool: "read",
            kind: "read",
            status: "completed",
            args: { path: "second.go" },
            output: "",
            outputReference: retainedRef,
            error: null,
          },
        ],
      };
      const entries: ActivitySpanEntry[] = [{ kind: "tool", part: foldedLead }];

      render(() => (
        <FindRoot>
          <ActivitySpanCard
            label="read"
            entries={entries}
            layout={layout}
            sessionId="s-fold"
            entryKey={`fold-${layout}`}
            renderToolEntry={(toolPart) => (
              <ToolPartCard part={toolPart()} layout={layout} sessionId="s-fold" projectId="proj-1" />
            )}
          />
        </FindRoot>
      ));

      const span = (await screen.findByTestId("activity-span-card")) as HTMLElement;
      expect(isDisclosureOpen(span)).toBe(false);
      expect(screen.queryAllByTestId("action-card")).toHaveLength(0);

      openFind();
      setFindQuery("RETAINED_FOLD_NEEDLE");
      await waitFor(() => expect(findController.matches()).toHaveLength(1), { timeout: 2000 });
      expect(searchChatContent).toHaveBeenCalledTimes(1);

      await waitFor(() => expect(isDisclosureOpen(span)).toBe(true));
      const card = screen.getByTestId("action-card");
      await waitFor(() => expect(isDisclosureOpen(card)).toBe(true));
      await waitFor(() => expect(sink).toHaveBeenCalled());
      expect(sink.mock.lastCall?.[0]).toMatchObject({
        kind: "chat-content",
        projectId: "proj-1",
        document: {
          kind: "tool",
          sessionId: "s-fold",
          toolCallId: "member-call",
          messageId: "m-member",
          revealOffset: 42,
          content: { kind: "retained", reference: retainedRef },
        },
      });

      closeFind();
      expect(isDisclosureOpen(span)).toBe(false);
    },
  );

  it("reveals retained output and retained args on a standalone closed action card", async () => {
    const sink = vi.fn();
    registerOpenFilesSurfaceSink(sink);
    const outRef = {
      field: "tool_output" as const,
      tool_call_id: "direct-call",
      sha256: "blob-out",
      size_bytes: 512,
      total_runes: 256,
      rows: 10,
    };
    const argRef = {
      field: "tool_args" as const,
      tool_call_id: "direct-call",
      sha256: "blob-arg",
      size_bytes: 256,
      total_runes: 128,
      rows: 4,
    };
    const client = stubClient({
      searchChatContent: vi.fn(async (_sid: string, _mid: string, ref: { field?: string }, query: string) => {
        if (query === "DIRECT_OUT_NEEDLE" && ref.field === "tool_output") {
          return { matches: [{ offset: 17, length: 6, preview: "DIRECT_OUT_NEEDLE" }] };
        }
        if (query === "DIRECT_ARG_NEEDLE" && ref.field === "tool_args") {
          return { matches: [{ offset: 9, length: 6, preview: "DIRECT_ARG_NEEDLE" }] };
        }
        return { matches: [] };
      }),
    });

    const directPart: ToolPartView = {
      id: "a1:direct-call",
      toolCallId: "direct-call",
      assistantMessageId: "a1",
      messageId: "m-direct",
      tool: "read",
      kind: "read",
      status: "completed",
      args: { path: "direct.go" },
      output: "",
      outputReference: outRef,
      argsReference: argRef,
      error: null,
    };

    render(() => (
      <FindRoot>
        <ToolPartCard
          part={directPart}
          layout="chat"
          sessionId="s-direct"
          projectId="proj-1"
          client={client}
        />
      </FindRoot>
    ));

    openFind();
    setFindQuery("DIRECT_OUT_NEEDLE");
    await waitFor(() => expect(sink).toHaveBeenCalled());
    expect(sink.mock.lastCall?.[0]).toMatchObject({
      kind: "chat-content",
      projectId: "proj-1",
      document: {
        sessionId: "s-direct",
        toolCallId: "direct-call",
        revealOffset: 17,
        content: { kind: "retained", reference: outRef },
      },
    });

    sink.mockClear();
    closeFind();
    openFind();
    setFindQuery("DIRECT_ARG_NEEDLE");
    await waitFor(() => expect(sink).toHaveBeenCalled());
    expect(sink.mock.lastCall?.[0]).toMatchObject({
      kind: "chat-content",
      projectId: "proj-1",
      document: {
        sessionId: "s-direct",
        toolCallId: "direct-call",
        revealOffset: 9,
        content: { kind: "retained", reference: argRef },
      },
    });
  });

  it("forwards an explicit client when searching a standalone command-activity card", async () => {
    const sink = vi.fn();
    registerOpenFilesSurfaceSink(sink);
    const cmdRef = {
      field: "tool_output" as const,
      tool_call_id: "cmd-subcall",
      sha256: "blob-cmd",
      size_bytes: 640,
      total_runes: 320,
      rows: 12,
    };
    const searchChatContent = vi.fn(async () => ({
      matches: [{ offset: 23, length: 6, preview: "CMD_CLIENT_NEEDLE" }],
    }));
    const client = stubClient({ searchChatContent });

    const subCall: ToolPartView = {
      id: "a1:cmd-subcall",
      toolCallId: "cmd-subcall",
      assistantMessageId: "a1",
      messageId: "m-cmd",
      tool: "command",
      kind: "command",
      status: "completed",
      args: { command: "go test ./..." },
      output: "",
      outputReference: cmdRef,
      error: null,
    };
    const fact = {
      id: "act-1",
      label: "Run tests",
      subject: "go test ./...",
      group: "Running commands",
      phase: "execute" as const,
      outcome: "Completed",
      state: "completed" as const,
      execution_ids: [],
      spans: [],
    };
    const cmdPart: ToolPartView = {
      ...subCall,
      id: "a1:cmd-host",
      toolCallId: "cmd-host",
      commandActivity: {
        fact,
        calls: [subCall],
        members: [fact],
        receiptScopes: [],
      },
    };

    render(() => (
      <FindRoot>
        <ToolPartCard
          part={cmdPart}
          layout="chat"
          sessionId="s-cmd"
          projectId="proj-1"
          client={client}
        />
      </FindRoot>
    ));

    openFind();
    setFindQuery("CMD_CLIENT_NEEDLE");
    await waitFor(() => expect(findController.matches()).toHaveLength(1), { timeout: 2000 });
    expect(searchChatContent).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(sink).toHaveBeenCalled());
    expect(sink.mock.lastCall?.[0]).toMatchObject({
      kind: "chat-content",
      projectId: "proj-1",
      document: {
        sessionId: "s-cmd",
        toolCallId: "cmd-subcall",
        revealOffset: 23,
        content: { kind: "retained", reference: cmdRef },
      },
    });
  });

});
