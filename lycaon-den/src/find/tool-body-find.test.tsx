import { afterEach, describe, expect, it, vi } from "vitest";
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
import { GenericToolCard } from "../components/tool/GenericToolCard.tsx";
import { registerOpenFilesSurfaceSink, resetOpenFilesSurfaceForTests } from "../platform/navigation/open-files-surface.ts";
import { ToolPartShell } from "../components/tool/ToolPartShell.tsx";
import { ToolPartCard } from "../components/tool/ToolPartCard.tsx";
import { ActivitySpanCard } from "../components/tool/ActivitySpanCard.tsx";
import type { ToolPartView } from "../chat/tool/tool-part-model.ts";
import type { ActivitySpanEntry } from "../chat/transcript/projection/transcript-item-model.ts";
import { clearTranscriptEntryMemory } from "../chat/transcript/presentation/transcript-entry.ts";

afterEach(() => {
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
    expect((card as HTMLDetailsElement).open).toBe(false);
    card.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));

    openFind();
    setFindQuery("TOOL_BODY_FIND_NEEDLE");
    expect(findController.matches().length).toBeGreaterThanOrEqual(1);
    expect(findController.collapsedCount()).toBeGreaterThanOrEqual(1);
    expect((card as HTMLDetailsElement).open).toBe(false);
  });

  it("expands on first next, restores on close", async () => {
    render(() => (
      <ToolPartShell part={part()} layout="chat" sessionId="s1">
        <p>TOOL_BODY_FIND_NEEDLE content</p>
      </ToolPartShell>
    ));

    const card = (await screen.findByTestId(
      "tool-part-card",
    )) as HTMLDetailsElement;
    card.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
    openFind();
    setFindQuery("TOOL_BODY_FIND_NEEDLE");
    expect(findController.collapsedCount()).toBeGreaterThanOrEqual(1);

    findNext();
    expect(card.open).toBe(true);
    expect(findController.collapsedCount()).toBe(0);
    expect(
      document.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`).length,
    ).toBeGreaterThanOrEqual(1);

    closeFind();
    expect(card.open).toBe(false);
  });

  it("drills a collapsed activity span and nested disclosure through temporary leases", async () => {
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
    )) as HTMLDetailsElement;
    const cards = (await screen.findAllByTestId(
      "tool-part-card",
    )) as HTMLDetailsElement[];
    expect(span.open).toBe(false);
    expect(cards.every((c) => !c.open)).toBe(true);

    openFind();
    setFindQuery("GROUP_NESTED_FIND_NEEDLE");
    expect(findController.collapsedCount()).toBeGreaterThanOrEqual(1);
    expect(span.open).toBe(false);

    findNext();
    expect(span.open).toBe(true);
    const nested = cards.find(
      (c) => c.getAttribute("data-tool-call-id") === "r2",
    );
    expect(nested?.open).toBe(true);
    expect(findController.collapsedCount()).toBe(0);

    closeFind();
    expect(span.open).toBe(false);
    expect(nested?.open).toBe(false);
  });

  it("opens linked inline output from a collapsed production tool card", async () => {
    const sink = vi.fn();
    registerOpenFilesSurfaceSink(sink);
    render(() => <FindRoot><GenericToolCard part={part()} layout="chat" sessionId="s1" projectId="project" /></FindRoot>);
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

});
