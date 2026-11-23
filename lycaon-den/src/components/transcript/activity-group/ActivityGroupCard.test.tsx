// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import type { Message } from "../../../api/types.ts";
import { stubFilesClient } from "../../../test/source-client-fixture.ts";
import { fileEditPreviewFixture } from "../../../test/file-edit-fixture.ts";
import { TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT } from "../../../chat/transcript/presentation/transcript-disclosure.tsx";
import { revealChicklet } from "../../../chat/transcript/presentation/transcript-reveal.ts";
import { transcriptViewportForSession } from "../../../chat/stream/transcript-viewport.tsx";
import { SessionTranscript } from "../transcript-viewport-test-harness.tsx";
import { resetSessionTranscriptChatTest } from "../session-transcript-chat-test-harness.ts";

const client = stubFilesClient();
vi.mock("../../../platform/connection/app-connection.ts", async (original) => ({
  ...(await original<typeof import("../../../platform/connection/app-connection.ts")>()),
  getLycaonClient: () => client,
}));

describe("ActivityGroupCard", () => {
  afterEach(() => {
    cleanup();
    return resetSessionTranscriptChatTest();
  });

  it("renders a uniform card shell in first-arrival sectionOrder without shifting earlier sections", async () => {
    const baseMessages: Message[] = [
      {
        id: "u1",
        ord: 1,
        role: "user",
        origin: "user",
        authority: "user",
        trust_tier: "trusted",
        content: "Fix tests",
        created_at: "2026-11-23T00:00:00Z",
      },
      {
        id: "a2",
        ord: 2,
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "",
        tool_calls: [{ id: "c1", name: "read", args: { path: "main.go" } }],
        created_at: "2026-11-23T00:00:01Z",
      },
      {
        id: "r3",
        ord: 3,
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "package main",
        tool_result: {
          assistant_message_id: "a2",
          tool_call_id: "c1",
          tool: "read",
          content: "package main",
          outcome: "completed",
        },
        created_at: "2026-11-23T00:00:02Z",
      },
    ];

    const [messages, setMessages] = createSignal<Message[]>(baseMessages);
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-uniform" projectId="p1" messages={messages()} />
    ));

    const group = container.querySelector<HTMLElement>('[data-testid="activity-group"]')!;
    expect(group).toBeTruthy();
    expect(group.getAttribute("data-sections")).toBe("activities");
    const firstSectionBefore = group.querySelector<HTMLElement>('[data-activity-group-section="activities"]')!;

    setMessages([
      ...baseMessages,
      {
        id: "a4",
        ord: 4,
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "",
        tool_calls: [{ id: "c2", name: "write", args: { path: "main.go" } }],
        created_at: "2026-11-23T00:00:03Z",
      },
      {
        id: "r5",
        ord: 5,
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "ok",
        tool_result: {
          assistant_message_id: "a4",
          tool_call_id: "c2",
          tool: "write",
          content: "ok",
          outcome: "completed",
          checkpoint_decision: {
            checkpoint_id: "cp-1",
            kind: "tool_approval",
            status: "approved",
            grant_title: "Allow once",
          },
          file_edit_previews: [fileEditPreviewFixture({ path: "main.go", before: "package main\n", after: "package main\n// ok\n" })],
        },
        created_at: "2026-11-23T00:00:04Z",
      },
    ]);
    await Promise.resolve();

    expect(group.getAttribute("data-sections")).toBe("activities,approvals,files");
    const firstSectionAfter = group.querySelector<HTMLElement>('[data-activity-group-section="activities"]')!;
    expect(firstSectionAfter).toBe(firstSectionBefore);
  });

  it("shows explicit wait counts and suppresses alarm failure chips on recovered intervals", () => {
    const messages: Message[] = [
      {
        id: "a1",
        ord: 1,
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "",
        tool_calls: [
          { id: "c-log", name: "command", args: { command: "cat server.log" } },
          { id: "c-err", name: "command", args: { command: "false" } },
          { id: "c-ok", name: "command", args: { command: "true" } },
          { id: "c-wait", name: "wait", args: { seconds: 5 } },
        ],
        created_at: "2026-11-23T00:00:00Z",
      },
      {
        id: "r-log",
        ord: 2,
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "Error: previous run failed, exit code 1",
        tool_result: {
          assistant_message_id: "a1",
          tool_call_id: "c-log",
          tool: "command",
          content: "Error: previous run failed, exit code 1",
          outcome: "completed",
          invocation: { owner: "coordinator", lifecycle: "foreground", status: "completed" },
        },
        created_at: "2026-11-23T00:00:01Z",
      },
      {
        id: "r-err",
        ord: 3,
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "patch failed",
        tool_result: {
          assistant_message_id: "a1",
          tool_call_id: "c-err",
          tool: "command",
          content: "patch failed",
          outcome: "error",
          invocation: { owner: "coordinator", lifecycle: "foreground", status: "error" },
        },
        created_at: "2026-11-23T00:00:02Z",
      },
      {
        id: "r-ok",
        ord: 4,
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "patched cleanly",
        tool_result: {
          assistant_message_id: "a1",
          tool_call_id: "c-ok",
          tool: "command",
          content: "patched cleanly",
          outcome: "completed",
          invocation: { owner: "coordinator", lifecycle: "foreground", status: "completed" },
        },
        created_at: "2026-11-23T00:00:03Z",
      },
      {
        id: "r-wait",
        ord: 5,
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "ready",
        tool_result: {
          assistant_message_id: "a1",
          tool_call_id: "c-wait",
          tool: "wait",
          content: "ready",
          outcome: "completed",
        },
        created_at: "2026-11-23T00:00:04Z",
      },
    ];

    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-recovered" messages={messages} />
    ));
    const span = container.querySelector<HTMLElement>('[data-testid="activity-span-card"]')!;
    expect(span.getAttribute("data-status")).toBe("done");
    expect(span.querySelector('[data-testid="activity-span-failed"]')).toBeNull();
    expect(span.querySelector('[data-testid="activity-span-count"]')?.textContent).toBe("3 commands · 1 wait");
  });

  it("compensates intra-group scroll offset when an upper section resizes above the reading line", () => {
    const messages: Message[] = [
      {
        id: "a1",
        ord: 1,
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "",
        tool_calls: [{ id: "c1", name: "write", args: { path: "a.go" } }],
        created_at: "2026-11-23T00:00:00Z",
      },
      {
        id: "r1",
        ord: 2,
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "ok",
        tool_result: {
          assistant_message_id: "a1",
          tool_call_id: "c1",
          tool: "write",
          content: "ok",
          outcome: "completed",
          file_edit_previews: [fileEditPreviewFixture({ path: "a.go", before: "old\n", after: "new\n" })],
        },
        created_at: "2026-11-23T00:00:01Z",
      },
    ];

    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-intra-scroll" messages={messages} />
    ));

    const controller = transcriptViewportForSession("s-intra-scroll")!;
    expect(controller).toBeTruthy();
    controller.stopFollowing();
    const shiftSpy = vi.spyOn(controller, "shiftVirtualContent").mockImplementation((d) => d);

    const stream = controller.stream()!;
    const group = container.querySelector<HTMLElement>('[data-testid="activity-group"]')!;
    const filesSection = group.querySelector<HTMLElement>('[data-activity-group-section="files"]')!;

    stream.getBoundingClientRect = () => ({ top: 100, bottom: 500, left: 0, right: 600, width: 600, height: 400 } as DOMRect);
    group.getBoundingClientRect = () => ({ top: 20, bottom: 380, left: 0, right: 600, width: 600, height: 360 } as DOMRect);

    let filesOffsetTop = 60;
    Object.defineProperty(filesSection, "offsetTop", {
      configurable: true,
      get: () => filesOffsetTop,
    });

    group.dispatchEvent(new Event(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, { bubbles: true }));
    expect(shiftSpy).not.toHaveBeenCalled();

    filesOffsetTop = 186;
    group.dispatchEvent(new Event(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, { bubbles: true }));
    expect(shiftSpy).toHaveBeenCalledWith(126, expect.any(Number));

    // When the entire group is above the reading line, outer virtualizer owns the shift.
    shiftSpy.mockClear();
    group.getBoundingClientRect = () => ({ top: -400, bottom: 90, left: 0, right: 600, width: 600, height: 490 } as DOMRect);
    filesOffsetTop = 240;
    group.dispatchEvent(new Event(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, { bubbles: true }));
    expect(shiftSpy).not.toHaveBeenCalled();
  });

  it("reveals and focuses a target toolCallId inside a closed >100-item Activities section without opening other sections", async () => {
    const calls = Array.from({ length: 120 }, (_, i) => ({
      id: `call-${i}`,
      name: "read",
      args: { path: `file_${i}.go` },
    }));
    const results: Message[] = calls.map((call, i) => ({
      id: `res-${i}`,
      ord: i + 2,
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content: "pkg",
      tool_result: {
        assistant_message_id: "a1",
        tool_call_id: call.id,
        tool: "read",
        content: "pkg",
        outcome: "completed",
        ...(i === 0
          ? { file_edit_previews: [fileEditPreviewFixture({ path: "file_0.go", before: "a\n", after: "b\n" })] }
          : {}),
      },
      created_at: "2026-11-23T00:00:01Z",
    }));
    const messages: Message[] = [
      {
        id: "a1",
        ord: 1,
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "",
        tool_calls: calls,
        created_at: "2026-11-23T00:00:00Z",
      },
      ...results,
    ];

    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-reveal-120" projectId="p1" messages={messages} />
    ));
    const stream = container.closest<HTMLElement>(".den-chat-stream") ?? container.querySelector<HTMLElement>(".den-chat-stream")!;
    const ok = await revealChicklet(stream, {
      sessionId: "s-reveal-120",
      anchor: { chicklet: "tool", anchorId: "call-115", persistExpansion: true },
    });
    expect(ok).toBe(true);

    const activitiesShell = container.querySelector<HTMLElement>('[data-testid="activity-span-card"]')!;
    const filesShell = container.querySelector<HTMLElement>('[data-testid="diff-group"]')!;
    expect(activitiesShell.getAttribute("data-expanded")).toBe("true");
    expect(filesShell.getAttribute("data-expanded")).toBe("false");

    await waitFor(() => {
      const target = container.querySelector<HTMLElement>('[data-tool-call-id="call-115"]');
      expect(target).toBeTruthy();
    });
  });

  it("navigates from Show activity on an open FileEditDiff to the producing activity while keeping Files edited open", async () => {
    const messages: Message[] = [
      {
        id: "a1",
        ord: 1,
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "",
        tool_calls: [{ id: "cmd-edit", name: "write", args: { path: "target.go" } }],
        created_at: "2026-11-23T00:00:00Z",
      },
      {
        id: "r1",
        ord: 2,
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "wrote target.go",
        tool_result: {
          assistant_message_id: "a1",
          tool_call_id: "cmd-edit",
          tool: "write",
          content: "wrote target.go",
          outcome: "completed",
          file_edit_previews: [fileEditPreviewFixture({ root_id: "main", path: "target.go", before: "a\n", after: "b\n" })],
        },
        created_at: "2026-11-23T00:00:01Z",
      },
    ];

    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-show-act" projectId="project" messages={messages} />
    ));

    const filesShell = container.querySelector<HTMLElement>('[data-testid="diff-group"]')!;
    fireEvent.click(filesShell.querySelector<HTMLElement>("button.den-transcript-disclosure-toggle")!);
    expect(filesShell.getAttribute("data-expanded")).toBe("true");

    const showActivityBtn = await waitFor(() => {
      const btn = container.querySelector<HTMLButtonElement>('[data-testid="file-edit-show-activity"]');
      expect(btn).toBeTruthy();
      return btn!;
    });
    fireEvent.click(showActivityBtn);

    await waitFor(() => {
      const activitiesShell = container.querySelector<HTMLElement>('[data-testid="activity-span-card"]')!;
      expect(activitiesShell.getAttribute("data-expanded")).toBe("true");
      expect(filesShell.getAttribute("data-expanded")).toBe("true");
    });
  });
});
