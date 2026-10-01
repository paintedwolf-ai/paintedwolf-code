import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import {
  resetSessionTranscriptChatTest,

  dispatchPair,
} from "./session-transcript-chat-test-harness.ts";

import {
  registerOpenSourceProjectLookup, registerOpenSourceSink, resetOpenSourceForTests,
} from "../../platform/navigation/open-source.ts";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { SessionTranscript } from "./transcript-viewport-test-harness.tsx";
import { messagesToTranscriptItems } from "../../chat/transcript/projection/transcript-items.ts";
import type { Message, WorkerTask } from "../../api/types.ts";
import { saveVerboseMode } from "../../settings/system/debug-prefs.ts";
import { workerTranscriptFixture } from "../../test/worker-transcript-fixture.ts";

describe("SessionTranscript workers and synthesis", () => {
  afterEach(resetSessionTranscriptChatTest);
  afterEach(resetOpenSourceForTests);

  it("waits for promotion, then opens the merged file in its project root", () => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    registerOpenSourceProjectLookup(() => ({ roots: [
      { id: "primary", path: "/project" },
      { id: "secondary", path: "/shared" },
    ] }));
    const [messages, setMessages] = createSignal<Message[]>(dispatchPair({
      assistantId: "dispatch", tcId: "tc", trId: "dispatched", agentType: "implementer",
      goal: "Update files", jobId: "job-1", ord: [1, 2],
    }));
    const [workers, setWorkers] = createSignal<WorkerTask[]>([{
      id: "job-1", parent_session_id: "s1", agent_type: "implementer",
      created_at: "t", status: "complete", merge_status: "pending",
    }]);
    const { container } = render(() => <SessionTranscript
      sessionId="s1" projectId="p1" messages={messages()} workers={workers()}
      workerTranscripts={{ "job-1": workerTranscriptFixture([{
        id: "overlay-write", ord: 1, role: "tool", origin: "tool", authority: "none",
        trust_tier: "untrusted", content: "", created_at: "t",
        tool_result: { content: "", file_edit_preview: fileEditPreviewFixture({ path: "new.ts", after: "overlay bytes" }) },
      }]) }}
    />);
    expect(container.querySelector('[data-testid="diff-group"]')).toBeNull();
    setWorkers((rows) => rows.map((row) => ({ ...row, merge_status: "merged" })));
    expect(container.querySelector('[data-testid="diff-group"]')).toBeNull();
    setMessages((rows) => [...rows, {
      id: "promotion", ord: 5, role: "tool", origin: "host", authority: "none",
      trust_tier: "trusted", content: "", created_at: "t5",
      tool_result: { content: "", promotion_previews: [
        fileEditPreviewFixture({ root_id: "secondary", path: "new.ts", after: "merged bytes" }),
        fileEditPreviewFixture({ root_id: "secondary", path: "deleted.ts", before: "old", after: "", deleted: true }),
      ] },
    }]);
    expect(container.querySelector('[data-testid="diff-group"]')?.getAttribute("data-files")).toBe("2");
    const links = container.querySelectorAll<HTMLButtonElement>('[data-testid="source-path-link"]');
    expect(links).toHaveLength(1);
    links[0]!.click();
    expect(sink).toHaveBeenCalledWith(expect.objectContaining({
      projectId: "p1", path: "new.ts", rootId: "secondary", absolutePath: "/shared/new.ts",
    }));
    expect(sink.mock.calls[0]![0]).not.toHaveProperty("jobId");
  });


  it.each<{
    name: string;
    messages: Message[];
    workers?: WorkerTask[];
    assert: (container: HTMLElement, onOpenWorker: ReturnType<typeof vi.fn>) => void;
  }>([
    {
      name: "labels the canonical worker card when the roster is empty",
      messages: dispatchPair({
        assistantId: "a1",
        tcId: "tc1",
        trId: "tr1",
        agentType: "implementer",
        goal: "Verify index.html",
        jobId: "job-cv",
      }),
      assert: (container, onOpenWorker) => {
        const group = container.querySelector('[data-testid="worker-group-card"]');
        expect(group).toBeTruthy();
        expect(group?.getAttribute("aria-label")).toBe("1 worker");
        const cards = container.querySelectorAll('[data-testid="task-card"]');
        expect(cards.length).toBe(1);
        expect(container.querySelector('[data-testid="activity-span-card"]')).toBeNull();
        expect(container.textContent).toContain("Worker · implementer");
        cards[0]?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
        expect(onOpenWorker).toHaveBeenCalledWith("job-cv");
      },
    },
    {
      name: "labels and opens each card from its own dispatch row",
      messages: [
        ...dispatchPair({
          assistantId: "a-research",
          tcId: "tc-research",
          trId: "tr-research",
          agentType: "web-researcher",
          goal: "Research browser game libraries",
          jobId: "job-research",
          ts: ["t1", "t2"],
			ord: [1, 2],
        }),
        {
          id: "a-verify",
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "Overlay promoted. Now verify the result.",
          created_at: "t3",
			ord: 3,
        },
        ...dispatchPair({
          assistantId: "a-dispatch",
          tcId: "tc-cv",
          trId: "tr-cv",
          agentType: "implementer",
          goal: "Verify the single-file build",
          jobId: "job-cv",
          ts: ["t5", "t6"],
			ord: [4, 5],
        }),
      ],
      assert: (container, onOpenWorker) => {
        const cards = container.querySelectorAll('[data-testid="task-card"]');
        expect(cards.length).toBe(2);
        expect(container.textContent).toContain("Worker · web-researcher");
        expect(container.textContent).toContain("Worker · implementer");
        cards[0]?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
        expect(onOpenWorker).toHaveBeenCalledWith("job-research");
      },
    },
    {
      name: "renders the canonical task card (not a worker-summary card)",
      messages: dispatchPair({
        assistantId: "a-research",
        tcId: "tc-research",
        trId: "tr-research",
        agentType: "web-researcher",
        goal: "Research browser game libraries",
        jobId: "job-research",
        ts: ["t1", "t2"],
      }),
      workers: [],
      assert: (container, onOpenWorker) => {
        expect(container.querySelector('[data-task-kind="worker-summary"]')).toBeNull();
        const taskCard = container.querySelector('[data-task-kind="task"]');
        expect(taskCard).toBeTruthy();
        expect(container.textContent).toContain("Worker · web-researcher");
        expect(taskCard?.classList.contains("den-task-card--clickable")).toBe(true);
        taskCard?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
        expect(onOpenWorker).toHaveBeenCalledWith("job-research");
      },
    },
  ])("worker card labels — $name", ({ messages, workers, assert }) => {
    const onOpenWorker = vi.fn();
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        messages={messages}
        sessionId="sess-1"
        workers={workers}
        onOpenWorker={onOpenWorker}
      />
    ));
    assert(container, onOpenWorker);
  });

  it("groups consecutive worker cards in an always-open clickable list", () => {
    const messages = [
      ...dispatchPair({
        assistantId: "a-one",
        tcId: "tc-one",
        trId: "tr-one",
        agentType: "path-explorer",
        goal: "Map the transcript model",
        jobId: "job-one",
        ord: [1, 2],
      }),
      ...dispatchPair({
        assistantId: "a-two",
        tcId: "tc-two",
        trId: "tr-two",
        agentType: "implementer",
        goal: "Update the transcript UI",
        jobId: "job-two",
        ord: [3, 4],
      }),
    ];
    const onOpenWorker = vi.fn();
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        messages={messages}
        sessionId="sess-1"
        onOpenWorker={onOpenWorker}
      />
    ));

    const group = container.querySelector('[data-testid="worker-group-card"]');
    expect(group).toBeTruthy();
    expect(group?.tagName).toBe("SECTION");
    expect(group?.querySelectorAll('[data-testid="task-card"]')).toHaveLength(2);

    const second = group?.querySelectorAll('[data-testid="task-card"]')[1];
    second?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(onOpenWorker).toHaveBeenCalledWith("job-two");
  });

  it("hides internal coordinator prose by default and shows it in verbose mode", async () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s1"
        messages={[
          { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "fix it", created_at: "t" },
          {
            id: "a1",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "rejected attempt",
            visibility: "internal",
            created_at: "t",
          },
        ]}
      />
    ));
    expect(container.textContent).not.toContain("rejected attempt");
    await saveVerboseMode(true);
    expect(container.textContent).toContain("rejected attempt");
  });

  it("renders a mid-turn agent_note after its tool card with Note badge and chicklet", () => {
    const messages: Message[] = [
		{ id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "Where is auth?", created_at: "t", ord: 1 },
      {
        id: "a-tools",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        created_at: "t",
		ord: 2,
        tool_calls: [
          { id: "call_read_1", name: "read", args: { path: "middleware.go" } },
        ],
      },
      {
        id: "t1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "package auth",
        created_at: "t",
		ord: 3,
        tool_result: {
          tool_call_id: "call_read_1",
          tool: "read",
          assistant_message_id: "a-tools",
          tool_args: { path: "middleware.go" },
          content: "package auth",
          outcome: "completed",
        },
      },
      {
        id: "note-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "agent_note",
        content: "Auth middleware lives in middleware.go.",
        created_at: "t",
		ord: 4,
        grounding: {
          traced: true,
          cited_evidence: [{ path: "middleware.go", line: 1 }],
          checks: [
            {
              id: "path_citations",
              label: "Path citations",
              status: "passed" as const,
              kind: "citation",
              summary: "1 citation(s) matched",
            },
          ],
        },
      },
    ];

    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-note" messages={messages} />
    ));

    expect(screen.getByTestId("agent-note-badge").textContent).toBe("Note");
    expect(
      container.querySelectorAll(
        '[data-testid="transcript-article-assistant"][data-kind="agent_note"]',
      ).length,
    ).toBe(1);
    expect(screen.getByTestId("citation-evidence-chicklet")).toBeTruthy();
    expect(container.querySelector('[data-testid="tool-part-card"]')).toBeTruthy();

    const kinds = messagesToTranscriptItems(messages, {
      layout: "chat",
    }).map((item) => item.kind);
    expect(kinds.indexOf("tool")).toBeGreaterThan(-1);
    expect(kinds.indexOf("assistant")).toBeGreaterThan(kinds.indexOf("tool"));
  });

  it("renders grounded coordinator synthesis markdown without viewport deferral", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        messages={[
          {
            id: "a1",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "### Build verified\n\n**Exit code `0`**.",
            created_at: "t",
            grounding: {
              traced: true,
              checks: [
                {
                  id: "typed_citations",
                  label: "Typed citations",
                  status: "passed" as const,
                  kind: "citation",
                  vacuous: true,
                  summary: "No typed citations in report",
                },
              ],
            },
          },
        ]}
      />
    ));
    const prose = container.querySelector(".assistant-prose");
    expect(prose?.querySelector("h3")?.textContent).toBe("Build verified");
    expect(screen.getByText("Exit code").tagName).toBe("STRONG");
    expect(prose?.textContent).not.toContain("###");
  });

  it("renders both history and newly delivered answers in full", () => {
    const body = "## Fresh answer\n\n" + "alpha ".repeat(80);
    const [messages, setMessages] = createSignal<Message[]>([
      { id: "hist", role: "assistant", origin: "model", authority: "none", trust_tier: "trusted", content: "## History\n\nbeta.", created_at: "t" },
    ]);
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s1" messages={messages()} />
    ));
    expect(container.querySelector(".assistant-prose")?.textContent).toContain("beta.");
    setMessages((prev) => [...prev, {
      id: "a1", role: "assistant", origin: "model", authority: "none", trust_tier: "trusted", content: body, created_at: "t",
    }]);
    expect(container.querySelector('[data-msg-id="a1"] .assistant-prose')?.textContent).toContain("alpha ".repeat(80).trim());
  });

  it("renders a collapsible evidence chicklet on a grounded coordinator summary", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        messages={[
          {
            id: "a1",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "The fix landed in `engine.py`.",
            created_at: "t",
            grounding: {
              traced: true,
              checks: [
                {
                  id: "path_citations",
                  label: "Path citations",
                  status: "passed" as const,
                  kind: "citation",
                  summary: "1 citation(s) matched leg evidence",
                  matched: ["`engine.py`"],
                },
              ],
            },
          },
        ]}
      />
    ));
    const chicklet = container.querySelector(
      '[data-testid="citation-evidence-chicklet"]',
    ) as HTMLDetailsElement;
    expect(chicklet).toBeTruthy();
    expect(chicklet.open).toBe(false);
    expect(chicklet.querySelector(".den-citation-grounding-chicklet--traced")).toBeTruthy();
    expect(
      container.querySelector('[data-testid="synthesis-evidence-section"]'),
    ).toBeNull();
    const panel = container.querySelector('[data-testid="citation-grounding-panel"]');
    expect(panel).toBeTruthy();
    expect(panel!.closest(".den-citation-evidence-chicklet-body")).toBeTruthy();

    chicklet.querySelector("summary")!.dispatchEvent(
      new MouseEvent("click", { bubbles: true }),
    );
    expect(chicklet.open).toBe(true);
    expect(container.querySelector('[data-testid="citation-grounding-panel"]')).toBeTruthy();
    expect(container.querySelector('[data-testid="citation-grounding-check"]')).toBeTruthy();
    expect(container.textContent).toContain("Path citations");
  });

  it("shows grounding as soon as it arrives", async () => {
    const realMatchMedia = window.matchMedia;
    window.matchMedia = ((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    })) as unknown as typeof window.matchMedia;
    const tick = () => new Promise((r) => setTimeout(r, 0));
    try {

      const base: Message = {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "## Verified\n\nThe fix landed in `engine.py`.",
        created_at: "t",
      };
      const prebuilt = messagesToTranscriptItems([base]);
      const [messages, setMessages] = createSignal<Message[]>([
        { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
        base,
      ]);
      const { container } = render(() => (
        <SessionTranscript
          layout="chat"
          sessionId="s1"
          messages={messages()}
          transcriptItems={prebuilt}
        />
      ));
      await tick();
      expect(container.querySelector(".assistant-prose")?.textContent).toContain("The fix landed");
      expect(
        container.querySelector('[data-testid="citation-evidence-chicklet"]'),
      ).toBeNull();

      setMessages([
        { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
        {
          ...base,
          grounding: {
            traced: true,
            checks: [
              {
                id: "path_citations",
                label: "Path citations",
                status: "passed" as const,
                kind: "citation",
                summary: "1 path matched",
                matched: ["`engine.py`"],
              },
            ],
          },
        },
      ]);
      await tick();
      expect(container.querySelector('[data-testid="citation-evidence-chicklet"]')).toBeTruthy();
    } finally {
      window.matchMedia = realMatchMedia;
    }
  });

  it("reveals the grounding dropdown when grounding lands after the answer streams", async () => {
    const base: Message = {
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "Synthesis answer.",
      created_at: "t",
    };
    const [messages, setMessages] = createSignal<Message[]>([base]);
    const { container } = render(() => (
      <SessionTranscript layout="chat" messages={messages()} />
    ));
    expect(
      container.querySelector('[data-testid="citation-evidence-chicklet"]'),
    ).toBeNull();

    setMessages([
      {
        ...base,
        grounding: {
          traced: true,
          checks: [
            {
              id: "cited_path",
              label: "Path citations",
              status: "passed" as const,
              kind: "citation",
              summary: "1 path matched",
              matched: ["src/a.go"],
            },
          ],
        },
      },
    ]);
    await Promise.resolve();

    expect(
      container.querySelector('[data-testid="citation-evidence-chicklet"]'),
    ).toBeTruthy();
  });

  it("renders cited_evidence only inside the collapsed chicklet body", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        messages={[
          {
            id: "a1",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "Synthesis cites leg evidence.",
            created_at: "t",
            grounding: {
              traced: true,
              cited_evidence: [{ handle: "leg-a:grep#1", path: "src/a.go", line: 10 }],
              checks: [
                {
                  id: "cited_evidence",
                  label: "Leg evidence handles",
                  status: "passed" as const,
                  kind: "citation",
                  summary: "1 handle(s) matched leg union ledger",
                  matched: ["src/a.go"],
                },
              ],
            },
          },
        ]}
      />
    ));
    const chicklet = container.querySelector(
      '[data-testid="citation-evidence-chicklet"]',
    ) as HTMLDetailsElement;
    expect(chicklet.open).toBe(false);
    expect(
      container.querySelector('[data-testid="synthesis-evidence-section"]'),
    ).toBeNull();
    expect(
      chicklet.querySelector('[data-testid="citation-grounding-panel"]')?.closest(
        ".den-citation-evidence-chicklet-body",
      ),
    ).toBeTruthy();
    chicklet.open = true;
    expect(container.textContent).toContain("Leg evidence handles");
    const citations = chicklet.querySelector(".den-citation-grounding-citations");
    expect(citations).toBeTruthy();
    const row = chicklet.querySelector('[data-testid="citation-grounding-citation"]');
    // The path cell, link or plain — this fixture carries no project, and the
    // assertion is about where the citation renders, not whether it opens.
    expect(
      row?.querySelector(
        '[data-testid="source-path-link"], .den-source-path-plain',
      )?.textContent,
    ).toBe("src/a.go:10");
    expect(citations?.textContent).not.toContain("leg-a:grep#1");
  });

  it("omits the evidence chicklet when grounding had nothing to verify", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        messages={[
          {
            id: "a1",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "Survey complete — no file citations in this summary.",
            created_at: "t",
            grounding: {
              traced: true,
              checks: [
                {
                  id: "typed_citations",
                  label: "Typed citations",
                  status: "passed" as const,
                  kind: "citation",
                  vacuous: true,
                  summary: "No typed citations in synthesis report",
                },
              ],
            },
          },
        ]}
      />
    ));
    expect(container.querySelector('[data-testid="citation-evidence-chicklet"]')).toBeNull();
    expect(container.querySelector('[data-testid="synthesis-evidence-section"]')).toBeNull();
  });

});
