import {
  resetSessionTranscriptChatTest,

} from "./session-transcript-chat-test-harness.ts";

import { createSignal } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { SessionTranscript } from "./transcript-viewport-test-harness.tsx";
import type { Message } from "../../api/types.ts";
import { saveVerboseMode } from "../../settings/system/debug-prefs.ts";
import { noteTranscriptEntryBaseline } from "../../chat/transcript/presentation/transcript-entry.ts";

describe("SessionTranscript draft rails", () => {
  afterEach(resetSessionTranscriptChatTest);

  it("renders a committed answer with a version rail above it when count > 1", () => {
    const messages: Message[] = [
      {
        id: "slot-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "draft",
        content: "final attempt body",
        draft_version_count: 3,
        draft_status: "committed",
        created_at: "t",
      },
    ];

    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s1" messages={messages} />
    ));
    const bubble = container.querySelector(".bubble--assistant");
    expect(bubble?.textContent).toContain("final attempt body");
    const rail = container.querySelector('[data-testid="draft-rail"]');
    expect(rail).toBeTruthy();
    expect(
      rail!.compareDocumentPosition(bubble!) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    const toggle = screen.getByTestId("draft-rail-toggle");
    expect(toggle.textContent).toContain("Versions (2)");
  });

  it("keeps the grounded answer's version rail with verbose off while mid-run drafts stay hidden", () => {
    const messages: Message[] = [
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "audit the repo", created_at: "t0" },
      {
        id: "step-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "draft",
        content: "Reading the build layout before I dispatch.",
        visibility: "transcript",
        draft_status: "committed",
        draft_version_count: 1,
        tool_calls: [{ id: "tc1", name: "read", args: { path: "README.md" } }],
        created_at: "t1",
      },
      {
        id: "answer-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "The build is green.",
        visibility: "transcript",
        draft_status: "committed",
        draft_version_count: 2,
        grounding: { traced: true, checks: [] },
        created_at: "t2",
      },
    ];

    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s1" messages={messages} />
    ));
    expect(container.textContent).not.toContain("Reading the build layout");
    const rails = container.querySelectorAll('[data-testid="draft-rail"]');
    expect(rails).toHaveLength(1);
    expect(screen.getByTestId("draft-rail-toggle").textContent).toContain(
      "Versions (1)",
    );
  });

  it("renders a committed orchestration step as a collapsed, expandable rail", async () => {
    await saveVerboseMode(true);
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s1"
        messages={[
          { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "audit the repo", created_at: "t0" },
          {
            id: "step-1",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            kind: "draft",
            content:
              "Reading the build layout\nbefore I dispatch the worker fan-out.",
            draft_status: "committed",
            visibility: "transcript",
            draft_version_count: 1,
            tool_calls: [{ id: "tc1", name: "read", args: { path: "README.md" } }],
            created_at: "t1",
          },
        ]}
      />
    ));
    expect(container.querySelector(".bubble--assistant")).toBeNull();
    const rail = container.querySelector('[data-testid="draft-rail"]');
    expect(rail).toBeTruthy();
    expect(rail?.getAttribute("data-draft-live")).toBe("false");
    const summary = container.querySelector('[data-testid="draft-rail-summary-body"]');
    expect(summary?.textContent).toContain("Reading the build layout");
    const toggle = screen.getByTestId("draft-rail-toggle");
    expect(toggle.textContent).toContain("Show");
    fireEvent.click(toggle);
    const expanded = container.querySelector('[data-testid="draft-rail-expanded-body"]');
    expect(expanded?.textContent).toContain(
      "dispatch the worker fan-out",
    );
  });

  it("keeps the final answer a bubble, not a rail, when it carries no tool_calls", () => {
    const messages: Message[] = [
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "summarize", created_at: "t0" },
      {
        id: "answer-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "draft",
        content: "## Summary\n\nThe build is green.",
        draft_status: "committed",
        visibility: "transcript",
        draft_version_count: 1,
        created_at: "t1",
      },
    ];

    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s1" messages={messages} />
    ));
    const bubble = container.querySelector(".bubble--assistant");
    expect(bubble?.textContent).toContain("Summary");
    expect(container.querySelector('[data-testid="draft-rail"]')).toBeNull();
  });

  it("renders the committed answer immediately beside its version history", () => {
    const messages: Message[] = [
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", created_at: "t0" },
      {
        id: "slot-stable",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "## Repo overview\n\nFinal synthesis body.",
        visibility: "transcript",
        draft_status: "committed",
        draft_version_count: 2,
        grounding: { traced: true, checks: [] },
        created_at: "t1",
      },
    ];

    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s1"
        messages={messages}
      />
    ));
    const bubble = container.querySelector(".bubble--assistant");
    expect(bubble?.textContent).toContain("Final synthesis body.");

    const rails = container.querySelectorAll('[data-testid="draft-rail"]');
    expect(rails).toHaveLength(1);
    expect(screen.getByTestId("draft-rail-toggle").textContent).toContain(
      "Versions (1)",
    );
  });

  it("keeps the version-history rail when grounding lands on a blank content frame", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s1"
        messages={[
          { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", created_at: "t0" },
          {
            id: "slot-stable",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "",
            visibility: "transcript",
            draft_status: "committed",
            draft_version_count: 2,
            grounding: { traced: true, checks: [] },
            created_at: "t1",
          },
        ]}
      />
    ));
    const rails = container.querySelectorAll('[data-testid="draft-rail"]');
    expect(rails).toHaveLength(1);
    expect(screen.getByTestId("draft-rail-toggle").textContent).toContain(
      "Versions (1)",
    );
  });

  it("keeps one stable draft rail across supersede patches while live", async () => {
    await saveVerboseMode(true);
    const [content, setContent] = createSignal("attempt one");
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s1"
        messages={[
          { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", created_at: "t0" },
          {
            id: "slot-stable",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            kind: "draft",
            content: content(),
            visibility: "internal",
            draft_version_count: 2,
            draft_status: "live",
            status: "streaming",
            created_at: "t",
          },
        ]}
      />
    ));
    expect(container.querySelectorAll('[data-testid="draft-rail"]')).toHaveLength(1);
    expect(container.querySelector(".bubble--assistant")).toBeNull();
    setContent("attempt two final");
    expect(container.querySelectorAll('[data-testid="draft-rail"]')).toHaveLength(1);
    expect(
      container
        .querySelector('[data-testid="draft-rail"]')
        ?.getAttribute("data-draft-live"),
    ).toBe("true");
  });

  it("omits an empty withdrawn draft with no prior attempts", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s1"
        messages={[
          {
            id: "slot-w",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            kind: "draft",
            content: "",
            draft_status: "withdrawn",
            draft_version_count: 1,
            created_at: "t",
          },
        ]}
      />
    ));
    expect(container.querySelector(".bubble--assistant")).toBeNull();
    expect(container.querySelector('[data-testid="draft-rail"]')).toBeNull();
  });

  it("streams an in-flight attempt into the live draft rail, not a bubble", async () => {
    await saveVerboseMode(true);
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s1"
        messages={[
          { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", created_at: "t" },
          {
            id: "a1",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "partial **bold**",
            visibility: "internal",
            status: "streaming",
            created_at: "t",
          },
        ]}
      />
    ));
    expect(container.querySelector(".bubble--assistant")).toBeNull();
    const rail = container.querySelector('[data-testid="draft-rail"]');
    expect(rail?.getAttribute("data-draft-live")).toBe("true");
    expect(
      container.querySelector('[data-testid="draft-rail-live-body"]'),
    ).toBeTruthy();

    expect(
      container.querySelector(".den-draft-rail-live-prose")?.textContent,
    ).toContain("partial");
    const liveBody = container.querySelector('[data-testid="draft-rail-live-body"]');
    expect(liveBody?.classList.contains("den-draft-rail-body--collapsed")).toBe(true);
  });

  it("keeps a settled orchestration draft visible in the rail while tools run", async () => {
    await saveVerboseMode(true);
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s1"
        messages={[
          { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", created_at: "t0" },
          {
            id: "slot-stable",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "Orchestration prose before the next tool batch.",
            visibility: "internal",
            draft_status: "live",
            tool_calls: [{ id: "tc1", name: "read", args: { path: "README.md" } }],
            created_at: "t1",
          },
        ]}
      />
    ));
    expect(container.querySelector(".bubble--assistant")).toBeNull();
    const rail = container.querySelector('[data-testid="draft-rail"]');
    expect(rail?.getAttribute("data-draft-live")).toBe("false");
    const summary = container.querySelector('[data-testid="draft-rail-summary-body"]');
    expect(summary).toBeTruthy();
    expect(summary?.textContent).toContain("Orchestration prose");
    expect(container.querySelector("strong")).toBeNull();
  });

  it("keeps the live draft rail node stable as tool rows are added mid-turn", async () => {
    await saveVerboseMode(true);
    const [messages, setMessages] = createSignal<Message[]>([
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", created_at: "t0" },
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "partial draft prose",
        visibility: "internal",
        status: "streaming",
        created_at: "t1",
      },
    ]);
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s1"
        messages={messages()}
      />
    ));
    const railBefore = container.querySelector('[data-testid="draft-rail"]');
    const bodyBefore = container.querySelector(
      '[data-testid="draft-rail-live-body"]',
    );
    expect(railBefore).toBeTruthy();
    expect(bodyBefore).toBeTruthy();

    setMessages((prev) => [
      ...prev.slice(0, -1),
      {
        ...prev[prev.length - 1]!,
        tool_calls: [{ id: "tc1", name: "read", args: { path: "a.go" } }],
      },
      {
        id: "tr1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "read output",
        created_at: "t2",
        tool_result: {
          content: "read output",
          tool: "read",
          tool_call_id: "tc1",
          outcome: "completed",
        },
      } as Message,
    ]);

    expect(container.querySelector('[data-testid="draft-rail"]')).toBe(railBefore);
    expect(
      container.querySelector('[data-testid="draft-rail-live-body"]') ??
        container.querySelector('[data-testid="draft-rail-summary-body"]'),
    ).toBeTruthy();
  });

  it("renders markdown when tool_calls arrive before llm idle", () => {
    const messages: Message[] = [
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", created_at: "t" },
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "Done **bold**",
        tool_calls: [{ id: "tc1", name: "read", args: { path: "x.go" } }],
        created_at: "t",
      },
    ];

    render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s1"
       
        messages={messages}
      />
    ));
    expect(screen.getByText("bold").tagName).toBe("STRONG");
  });

  it("renders no assistant bubble before first message SSE while llm turn active", () => {
    noteTranscriptEntryBaseline("s1", ["u1"]);
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s1"
        messages={[{ id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", created_at: "t" }]}
      />
    ));
    expect(container.querySelector(".bubble--assistant")).toBeNull();
    const userBubble = container.querySelector(".bubble--user");
    expect(userBubble?.classList.contains("den-enter-fade")).toBe(false);
  });

  it("upgrades generic stream stub to worker card when task name arrives", async () => {
    const assistantId = "a-stream-task";
    const [messages, setMessages] = createSignal<Message[]>([
      {
        id: assistantId,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "functions.task:6", name: "", args: {} }],
        created_at: "t",
      },
    ]);

    const { container } = render(() => (
      <SessionTranscript layout="chat" messages={messages()} sessionId="s1" />
    ));
    expect(container.querySelector('[data-testid="task-card"]')).toBeNull();

    setMessages([
      {
        id: assistantId,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [
          {
            id: "functions.task:6",
            name: "task",
            args: {
              agent_type: "web-researcher",
              brief: { goal: "Research the latest cutting-edge AI trends", done_when: ["Return results."] },
            },
          },
        ],
        created_at: "t",
      },
      {
        id: "task-result-6",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"job_id":"job-6","status":"enqueued"}',
        tool_result: {
          content: '{"job_id":"job-6","status":"enqueued"}',
          tool: "task",
          tool_call_id: "functions.task:6",
          assistant_message_id: assistantId,
          job_id: "job-6",
          tool_args: {
            agent_type: "web-researcher",
            brief: { goal: "Research the latest cutting-edge AI trends", done_when: ["Return results."] },
          },
        },
        created_at: "t",
      },
    ]);
    await Promise.resolve();

    expect(container.querySelector('[data-testid="task-card"]')).toBeTruthy();
    expect(container.textContent).toContain(
      "Research the latest cutting-edge AI trends",
    );
    expect(container.querySelector('[data-testid="tool-part-card"]')).toBeNull();
  });
});
