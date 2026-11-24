import { resetSessionTranscriptChatTest } from "./session-transcript-chat-test-harness.ts";

import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import { SessionTranscript } from "./transcript-viewport-test-harness.tsx";
import type { CommandActivity, CommandProgramReceipt, Message } from "../../api/types.ts";
import { noteToolCallShownInFlight } from "../../chat/tool/tool-transcript-visibility.ts";
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
import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import { stubFilesClient } from "../../test/source-client-fixture.ts";

const client = stubFilesClient();
vi.mock("../../platform/connection/app-connection.ts", async (original) => ({
  ...(await original<typeof import("../../platform/connection/app-connection.ts")>()),
  getLycaonClient: () => client,
}));

function stamp(ord: number, seq = ord) {
  return {
    ord,
    seq,
    created_at: new Date(Date.UTC(2026, 9, 30, 12, 0, ord % 60)).toISOString(),
  };
}

function programReceipt(
  id: string,
  activities: CommandActivity[],
  state: "running" | "completed" | "failed" = "completed",
): CommandProgramReceipt {
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

function buildMultiIntervalTurnFixture(baseOrd = 1297): Message[] {
  const at = (offset: number) => stamp(baseOrd + offset);
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
      ...at(0),
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
      ...at(1),
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
      ...at(2),
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
        steps: [{ label: "Generate patch", state: "pending" }],
        changes: [],
      },
      ...at(15),
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
        content: "{}",
        outcome: "completed",
        command_program: programReceipt("prog-115", [act115]),
        file_edit_previews: [
          fileEditPreviewFixture({ path: "pkg/late_from_115.go", before: "v0\n", after: "v1\n" }),
        ],
      },
      ...at(17),
    },
    {
      id: "a-320",
      role: "assistant",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "",
      tool_calls: [
        { id: "call-120", name: "read", args: { path: "pkg/alpha.go" } },
        { id: "call-122", name: "command", args: { command: "rm -rf /outside/protected" } },
        { id: "call-348", name: "command", args: { command: "apply-patch" } },
      ],
      ...at(23),
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
        tool: "read",
        assistant_message_id: "a-320",
        content: "package alpha\n",
        outcome: "completed",
      },
      ...at(24),
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
        outcome: "rejected",
        checkpoint_decision: {
          checkpoint_id: "cp-122",
          kind: "tool_approval",
          status: "approved",
          tool: "command",
          subject: "/outside/protected",
          grant_title: "Allow once",
        },
        command_program: programReceipt(
          "prog-122",
          [
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
          ],
          "failed",
        ),
      },
      ...at(26),
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
          fileEditPreviewFixture({
            path: "pkg/alpha.go",
            before: "package alpha\n",
            after: "package alpha\n// updated\n",
          }),
          fileEditPreviewFixture({
            path: "pkg/beta.go",
            before: "package beta\n",
            after: "package beta\n// updated\n",
          }),
        ],
      },
      ...at(51),
    },
    {
      id: "carrier-363",
      role: "assistant",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "Applied the three-file patch; running verification.",
      tool_calls: [{ id: "call-wait-365", name: "wait", args: { reason: "Waiting for verification pass" } }],
      ...at(66),
    },
    {
      id: "note-364",
      role: "assistant",
      kind: "agent_note",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "Applied the three-file patch; running verification.",
      grounding: {
        traced: true,
        host_assembled: false,
        observed_path_count: 2,
        observed_paths_sample: ["pkg/alpha.go", "pkg/beta.go"],
      },
      ...at(67),
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
        content: "{}",
        outcome: "completed",
      },
      ...at(68),
    },
    {
      id: "wake-366",
      role: "user",
      kind: "host_loop_wake",
      visibility: "internal",
      origin: "host",
      authority: "system",
      trust_tier: "trusted",
      content: "wake",
      ...at(69),
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
      ...at(73),
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
        content: "Error: fixture string inside passing log read\n",
        outcome: "completed",
      },
      ...at(74),
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
        content: "FAIL",
        outcome: "error",
      },
      ...at(75),
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
        content: "ok",
        outcome: "completed",
      },
      ...at(83),
    },
    {
      id: "a-381",
      role: "assistant",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "All three updates are applied and verified.",
      ...at(84),
    },
  ];
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
    };
  });
}

describe("SessionTranscript stacked activity groups", () => {
  afterEach(resetSessionTranscriptChatTest);

  it("streams multi-interval work without moving earlier headers and matches cold, paged, and evicted windows", async () => {
    const sessionId = "s-replay-stack";
    noteToolCallShownInFlight(sessionId, "a-298:call-115");
    const turn = buildMultiIntervalTurnFixture(1297);
    const projectChat = (rows: readonly Message[]) =>
      createTranscriptDisplayProjector()(rows, undefined, { layout: "chat", sessionId })[0] ?? [];

    const [messages, setMessages] = createSignal<Message[]>(turn.slice(0, 1));
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId={sessionId} projectId="p1" messages={messages()} />
    ));

    let interval2FirstSection: HTMLElement | null = null;
    let interval2FirstTop: number | null = null;

    for (let len = 2; len <= turn.length; len++) {
      setMessages(turn.slice(0, len));
      await Promise.resolve();

      const cards = [...container.querySelectorAll<HTMLElement>('[data-testid="activity-group-card"]')];
      const second = cards[1];
      if (second) {
        const firstSection = second.querySelector<HTMLElement>(".den-activity-group-section");
        if (firstSection && !interval2FirstSection) {
          interval2FirstSection = firstSection;
          interval2FirstTop = firstSection.getBoundingClientRect().top;
        } else if (firstSection && interval2FirstSection) {
          expect(firstSection).toBe(interval2FirstSection);
          expect(firstSection.getBoundingClientRect().top).toBe(interval2FirstTop);
        }
      }
    }

    const streamedItems = projectChat(messages());
    const coldWindow = installTailWindow(
      emptyTranscriptWindow(),
      { messages: turn, hasMoreBefore: true, hasMoreAfter: false },
      { resetPages: true },
    );
    const coldItems = projectChat(materializeTranscriptWindow(coldWindow));
    expect(streamedItems.map((i) => ({ kind: i.kind, key: i.key }))).toEqual(
      coldItems.map((i) => ({ kind: i.kind, key: i.key })),
    );

    // Tail merge, older-page prepend, and >500-ord eviction keep the identical tail groups.
    let merged = emptyTranscriptWindow();
    for (let idx = 0; idx < turn.length; idx++) {
      const slice = turn.slice(Math.max(0, idx - 1), idx + 1);
      merged = mergeIntoTail(
        merged,
        { messages: slice, hasMoreBefore: true, hasMoreAfter: false },
        turn[idx]!.seq ?? 0,
      );
    }
    expect(projectChat(materializeTranscriptWindow(merged)).map((i) => i.key)).toEqual(
      coldItems.map((i) => i.key),
    );

    let paged = coldWindow;
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
    const evictedTailGroups = projectChat(evictedRows)
      .filter((item) => item.kind === "activity_group")
      .slice(-3);
    const coldGroups = coldItems.filter((item) => item.kind === "activity_group");
    expect(evictedTailGroups.map((g) => ({ key: g.key, sectionOrder: g.sectionOrder }))).toEqual(
      coldGroups.map((g) => ({ key: g.key, sectionOrder: g.sectionOrder })),
    );

    const cards = [...container.querySelectorAll<HTMLElement>('[data-testid="activity-group-card"]')];
    expect(cards).toHaveLength(3);
    expect(cards[0]!.getAttribute("data-sections")).toBe("activities");
    // Late diff from call-115 arrives at ord+17 before r-120 (activities) and call-348 (approvals).
    expect(cards[1]!.getAttribute("data-sections")).toBe("files,activities,approvals");
    expect(cards[2]!.getAttribute("data-sections")).toBe("activities");

    const approvalChicklet = cards[1]!.querySelector('[data-testid="checkpoint-decision-chicklet"]');
    expect(approvalChicklet?.getAttribute("data-status")).toBe("approved");
    expect(approvalChicklet?.textContent).toContain("Allowed");
    expect(cards[1]!.querySelector('[data-testid="activity-span-card"]')?.getAttribute("data-status")).toBe("done");
    fireEvent.click(cards[1]!.querySelector<HTMLButtonElement>('[data-testid="activity-span-card"] button')!);
    expect(cards[1]!.querySelector('[data-tool-call-id="call-122"]')?.getAttribute("data-status")).toBe("error");
    expect(cards[1]!.textContent).toContain("Failed");

    expect(cards[2]!.querySelector('[data-testid="activity-span-card"]')?.getAttribute("data-status")).toBe("done");
    expect(cards[2]!.textContent).toContain("1 wait");

    // Cross-group Show activity from late_from_115.go in Interval 2 reveals call-115 inside Interval 1.
    fireEvent.click(
      cards[1]!.querySelector<HTMLButtonElement>('[data-testid="diff-group"] button.den-transcript-disclosure-toggle')!,
    );
    const lateDiff = [...cards[1]!.querySelectorAll<HTMLElement>('[data-testid="file-edit-diff"]')].find((el) =>
      el.textContent?.includes("late_from_115.go"),
    );
    expect(lateDiff).toBeTruthy();
    fireEvent.click(lateDiff!.querySelector<HTMLButtonElement>('[data-testid="file-edit-diff-toggle"]')!);
    const showActivityBtn = await waitFor(() => {
      const btn = lateDiff!.querySelector<HTMLButtonElement>('[data-testid="file-edit-show-activity"]');
      expect(btn).toBeTruthy();
      return btn!;
    });
    fireEvent.click(showActivityBtn);
    await waitFor(() => {
      expect(cards[0]!.querySelector('[data-tool-call-id="call-115"]')).toBeTruthy();
    });
  });
});
