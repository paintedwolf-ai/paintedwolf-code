import { resetSessionTranscriptChatTest } from "./session-transcript-chat-test-harness.ts";

import { createSignal } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import { SessionTranscript } from "./transcript-viewport-test-harness.tsx";
import type { CommandActivity, CommandProgramReceipt, Message } from "../../api/types.ts";
import { toolApprovalFixture } from "../../chat/checkpoint/approval-test-fixtures.ts";
import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import {
  DEFAULT_TRANSCRIPT_PAGE_LIMIT,
  TRANSCRIPT_RESIDENT_ORD_BUDGET,
  emptyTranscriptWindow,
  installTailWindow,
  loadOlderWindow,
  materializeTranscriptWindow,
  mergeIntoTail,
} from "../../chat/transcript/layout/transcript-window.ts";
import { createTranscriptDisplayProjector } from "../../chat/transcript/projection/transcript-display-projection.ts";

function stamp(ord: number, seq = ord) {
  return {
    ord,
    seq,
    created_at: new Date(Date.UTC(2026, 9, 30, 12, 0, ord % 60)).toISOString(),
  };
}

function programReceipt(id: string, activities: CommandActivity[], state: "running" | "completed" | "failed" = "completed"): CommandProgramReceipt {
  return {
    id,
    parent_receipt_id: `${id}-parent`,
    language_version: 1,
    catalog_digest: "catalog",
    source_digest: "source",
    source_ref: "source",
    state,
    detail_ref: "detail",
    progress_state: "unchanged",
    started_at: "2026-10-30T12:00:00Z",
    activities,
  };
}

function buildMultiIntervalTurnFixture(): Message[] {
  const act115: CommandActivity = {
    id: "act-115",
    group: "Running programs",
    label: "Run generator",
    subject: "gen-patch",
    directory: ".",
    state: "completed",
    outcome: "Completed",
    execution_ids: ["e-115"],
    spans: [],
    steps: [],
  };
  const act348: CommandActivity[] = [
    {
      id: "act-348-patch",
      group: "Editing files",
      label: "Write patch",
      subject: "/private/tmp/st03-gen.patch",
      directory: ".",
      state: "completed",
      outcome: "Completed",
      execution_ids: ["e-348-1"],
      spans: [],
      steps: [],
    },
    {
      id: "act-348-alpha",
      group: "Editing files",
      label: "Apply alpha",
      subject: "pkg/alpha.go",
      directory: ".",
      state: "completed",
      outcome: "Completed",
      execution_ids: ["e-348-2"],
      spans: [],
      steps: [],
    },
  ];

  return [
    {
      id: "u-297",
      role: "user",
      origin: "user",
      authority: "user",
      trust_tier: "trusted",
      content: "Generate and apply the three-file update.",
      ...stamp(297),
    },
    {
      id: "a-298",
      role: "assistant",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "",
      tool_calls: [
        { id: "call-progress", name: "update_progress", args: { summary: "Starting patch generation" } },
        { id: "call-115", name: "command", args: { command: "gen-patch > /tmp/late.txt" } },
      ],
      ...stamp(298),
    },
    {
      id: "r-progress",
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content: "{}",
      tool_result: {
        tool_call_id: "call-progress",
        tool: "update_progress",
        assistant_message_id: "a-298",
        content: "{}",
        outcome: "completed",
      },
      ...stamp(299),
    },
    {
      id: "p-312",
      role: "system",
      kind: "progress_update",
      origin: "host",
      authority: "system",
      trust_tier: "trusted",
      content: "",
      progress_update: {
        initial: true,
        summary: "Generating patch",
        steps: [{ id: "s1", label: "Generate patch", state: "pending" }],
      },
      ...stamp(312),
    },
    {
      id: "r-115",
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content: "{}",
      tool_result: {
        tool_call_id: "call-115",
        tool: "command",
        assistant_message_id: "a-298",
        shown_in_flight: true,
        content: "{}",
        outcome: "completed",
        command_program: programReceipt("prog-115", [act115]),
        file_edit_previews: [fileEditPreviewFixture({ path: "pkg/late_from_115.go", before: "v0\n", after: "v1\n" })],
      },
      ...stamp(314),
    },
    {
      id: "a-320",
      role: "assistant",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "",
      tool_calls: [
        { id: "call-120", name: "read_file", args: { path: "pkg/alpha.go" } },
        { id: "call-122", name: "command", args: { command: "rm -rf /outside/protected" } },
        { id: "call-348", name: "command", args: { command: "apply-patch" } },
      ],
      ...stamp(320),
    },
    {
      id: "r-120",
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content: "package alpha\n",
      tool_result: {
        tool_call_id: "call-120",
        tool: "read_file",
        assistant_message_id: "a-320",
        shown_in_flight: true,
        content: "package alpha\n",
        outcome: "completed",
      },
      ...stamp(321),
    },
    {
      id: "cp-122",
      role: "system",
      kind: "checkpoint",
      origin: "host",
      authority: "system",
      trust_tier: "trusted",
      content: "",
      checkpoint_meta: {
        id: "cp-122",
        kind: "tool_approval",
        state: "approved",
        title: "Allow protected command",
        parent_message_id: "a-320",
        result_message_id: "r-122",
        call_index: 1,
        tool_approval: toolApprovalFixture({ command: "rm -rf /outside/protected", title: "Allow protected command" }),
      },
      ...stamp(322),
    },
    {
      id: "r-122",
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content: "refused by host sandbox",
      tool_result: {
        tool_call_id: "call-122",
        tool: "command",
        assistant_message_id: "a-320",
        content: "refused by host sandbox",
        outcome: "error",
        command_program: programReceipt("prog-122", [
          {
            id: "act-122",
            group: "Running programs",
            label: "Run command",
            subject: "rm -rf /outside/protected",
            directory: ".",
            state: "failed",
            outcome: "Refused",
            execution_ids: ["e-122"],
            spans: [],
            steps: [],
          },
        ], "failed"),
      },
      ...stamp(323),
    },
    {
      id: "r-348",
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content: "{}",
      tool_result: {
        tool_call_id: "call-348",
        tool: "command",
        assistant_message_id: "a-320",
        content: "{}",
        outcome: "completed",
        command_program: programReceipt("prog-348", act348),
        file_edit_previews: [
          fileEditPreviewFixture({ path: "/private/tmp/st03-gen.patch", after: "--- a\n+++ b\n" }),
          fileEditPreviewFixture({ path: "pkg/alpha.go", before: "package alpha\n", after: "package alpha\n// updated\n" }),
          fileEditPreviewFixture({ path: "pkg/beta.go", before: "package beta\n", after: "package beta\n// updated\n" }),
        ],
      },
      ...stamp(348),
    },
    {
      id: "carrier-363",
      role: "assistant",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "",
      tool_calls: [{ id: "call-wait-365", name: "wait", args: { reason: "Waiting for verification pass" } }],
      ...stamp(363),
    },
    {
      id: "note-364",
      role: "assistant",
      kind: "agent_note",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "Applied the three-file patch; running verification.",
      assistant_note_for_message_id: "carrier-363",
      grounding: { traced: true, host_assembled: false, observed_path_count: 2, observed_paths_sample: ["pkg/alpha.go", "pkg/beta.go"] },
      ...stamp(364),
    },
    {
      id: "r-wait-365",
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content: "{}",
      tool_result: {
        tool_call_id: "call-wait-365",
        tool: "wait",
        assistant_message_id: "carrier-363",
        shown_in_flight: true,
        content: "{}",
        outcome: "completed",
      },
      ...stamp(365),
    },
    {
      id: "wake-366",
      role: "system",
      kind: "host_loop_wake",
      visibility: "internal",
      origin: "host",
      authority: "system",
      trust_tier: "trusted",
      content: "wake",
      ...stamp(366),
    },
    {
      id: "a-370",
      role: "assistant",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "",
      tool_calls: [
        { id: "call-141", name: "command", args: { command: "cat test.log" } },
        { id: "call-144", name: "command", args: { command: "go test ./..." } },
        { id: "call-145", name: "command", args: { command: "go test ./pkg/..." } },
      ],
      ...stamp(370),
    },
    {
      id: "r-141",
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content: "Error: fixture string inside passing log read\n",
      tool_result: {
        tool_call_id: "call-141",
        tool: "command",
        assistant_message_id: "a-370",
        shown_in_flight: true,
        content: "Error: fixture string inside passing log read\n",
        outcome: "completed",
      },
      ...stamp(371),
    },
    {
      id: "r-144",
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content: "FAIL",
      tool_result: {
        tool_call_id: "call-144",
        tool: "command",
        assistant_message_id: "a-370",
        shown_in_flight: true,
        content: "FAIL",
        outcome: "error",
      },
      ...stamp(372),
    },
    {
      id: "r-145",
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content: "ok",
      tool_result: {
        tool_call_id: "call-145",
        tool: "command",
        assistant_message_id: "a-370",
        shown_in_flight: true,
        content: "ok",
        outcome: "completed",
      },
      ...stamp(380),
    },
    {
      id: "a-381",
      role: "assistant",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "All three updates are applied and verified.",
      ...stamp(381),
    },
  ] as Message[];
}

function fillerMessages(startOrd: number, count: number): Message[] {
  return Array.from({ length: count }, (_, idx) => {
    const ord = startOrd + idx;
    const isUser = ord % 2 === 1;
    return {
      id: `hist-${ord}`,
      role: isUser ? "user" : "assistant",
      origin: isUser ? "user" : "model",
      authority: isUser ? "user" : "none",
      trust_tier: "trusted",
      content: `history row ${ord}`,
      ...stamp(ord),
    } as Message;
  });
}

describe("SessionTranscript stacked activity groups", () => {
  afterEach(resetSessionTranscriptChatTest);

  it("streams multi-interval work without moving earlier section headers and matches cold, paged, and evicted windows", async () => {
    const turn = buildMultiIntervalTurnFixture();
    const projectChat = (rows: readonly Message[]) =>
      createTranscriptDisplayProjector()(rows, undefined, { layout: "chat" })[0] ?? [];
    const [messages, setMessages] = createSignal<Message[]>(turn.slice(0, 1));
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-replay-stack" messages={messages()} />
    ));

    let interval2ActivitiesBtn: HTMLElement | null = null;
    let interval2ActivitiesTop: number | null = null;
    let interval2ApprovalsBtn: HTMLElement | null = null;

    for (let len = 2; len <= turn.length; len++) {
      setMessages(turn.slice(0, len));
      await Promise.resolve();

      const cards = [...container.querySelectorAll<HTMLElement>('[data-testid="activity-group-card"]')];
      const second = cards[1];
      if (second) {
        const actBtn = second.querySelector<HTMLElement>('[data-testid="activity-span-card"] button');
        if (actBtn && !interval2ActivitiesBtn) {
          interval2ActivitiesBtn = actBtn;
          interval2ActivitiesTop = actBtn.getBoundingClientRect().top;
        } else if (actBtn && interval2ActivitiesBtn) {
          expect(actBtn).toBe(interval2ActivitiesBtn);
          expect(actBtn.getBoundingClientRect().top).toBe(interval2ActivitiesTop);
        }
        const appHeader = second.querySelector<HTMLElement>('[data-testid="checkpoint-decision-header"]');
        if (appHeader && !interval2ApprovalsBtn) {
          interval2ApprovalsBtn = appHeader;
        } else if (appHeader && interval2ApprovalsBtn) {
          expect(appHeader).toBe(interval2ApprovalsBtn);
        }
      }
    }

    const streamedItems = projectChat(messages());
    const coldWindow = installTailWindow(emptyTranscriptWindow(), {
      messages: turn,
      hasMoreBefore: false,
      hasMoreAfter: false,
    }, { resetPages: true });
    const coldItems = projectChat(materializeTranscriptWindow(coldWindow));
    expect(streamedItems.map((i) => ({ kind: i.kind, key: i.key }))).toEqual(
      coldItems.map((i) => ({ kind: i.kind, key: i.key })),
    );

    // Tail merge and older-page prepend + >500-ord eviction converge on the identical tail groups.
    let paged = emptyTranscriptWindow();
    for (const msg of turn) {
      paged = mergeIntoTail(paged, { messages: [msg], hasMoreBefore: true, hasMoreAfter: false }, msg.seq ?? 0);
    }
    const history = fillerMessages(1, TRANSCRIPT_RESIDENT_ORD_BUDGET + DEFAULT_TRANSCRIPT_PAGE_LIMIT + 40);
    for (let end = history.length; end > 0; end -= DEFAULT_TRANSCRIPT_PAGE_LIMIT) {
      const start = Math.max(0, end - DEFAULT_TRANSCRIPT_PAGE_LIMIT);
      paged = loadOlderWindow(paged, {
        messages: history.slice(start, end),
        hasMoreBefore: start > 0,
        hasMoreAfter: true,
      });
    }
    const evictedRows = materializeTranscriptWindow(paged);
    expect(evictedRows.length).toBeLessThan(history.length + turn.length);
    const evictedTailGroups = projectChat(evictedRows).filter((item) => item.kind === "activity_group");
    const coldGroups = coldItems.filter((item) => item.kind === "activity_group");
    expect(evictedTailGroups.map((g) => ({ key: g.key, sectionOrder: g.sectionOrder }))).toEqual(
      coldGroups.map((g) => ({ key: g.key, sectionOrder: g.sectionOrder })),
    );

    // Verify rendered structure across the three intervals.
    const cards = [...container.querySelectorAll<HTMLElement>('[data-testid="activity-group-card"]')];
    expect(cards).toHaveLength(3);
    expect(cards[0]!.getAttribute("data-sections")).toBe("activities");
    // Late file edit from call-115 arrived at ord 314 after progress_update 312, so Interval 2 opens with files -> activities -> approvals -> files deduplicated by first-arrival:
    expect(cards[1]!.getAttribute("data-sections")).toBe("files,activities,approvals");
    expect(cards[2]!.getAttribute("data-sections")).toBe("activities");

    // Interval 2 keeps Allowed approval distinct from Refused execution.
    expect(cards[1]!.querySelector('[data-testid="checkpoint-decision-verdict"]')?.textContent).toContain("Allowed");
    fireEvent.click(cards[1]!.querySelector<HTMLButtonElement>('[data-testid="activity-span-card"] button')!);
    expect(cards[1]!.textContent).toContain("Refused");

    // Interval 3 recovered after call-144 via call-145 and carries wait count in quiet/verbose modes.
    expect(cards[2]!.querySelector('[data-testid="activity-span-card"]')?.getAttribute("data-status")).toBe("done");
    expect(cards[2]!.textContent).toContain("1 wait");

    // Cross-group "Show activity" from late_from_115.go in Interval 2 reveals call-115 inside Interval 1!
    fireEvent.click(cards[1]!.querySelector<HTMLButtonElement>('[data-testid="diff-group"] button.den-transcript-disclosure-toggle')!);
    const lateDiff = [...cards[1]!.querySelectorAll<HTMLElement>('[data-testid="file-edit-diff"]')].find((el) =>
      el.textContent?.includes("late_from_115.go"),
    );
    expect(lateDiff).toBeTruthy();
    fireEvent.click(lateDiff!.querySelector<HTMLButtonElement>("button.den-file-edit-header")!);
    const showActivityBtn = lateDiff!.querySelector<HTMLButtonElement>('[data-testid="source-diff-show-activity"]');
    expect(showActivityBtn).toBeTruthy();
    fireEvent.click(showActivityBtn!);
    expect(cards[0]!.querySelector('[data-tool-call-id="call-115"]')).toBeTruthy();
  });
});
