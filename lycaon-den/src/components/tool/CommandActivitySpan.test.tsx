import { createSignal } from "solid-js";
import { expect, it, vi } from "vitest";
import { fireEvent, render, waitFor, within } from "@solidjs/testing-library";
import type { CommandActivity, CommandProgramReceipt } from "../../api/types.ts";
import type { ActivitySpanEntry } from "../../chat/transcript/projection/transcript-item-model.ts";
import { clearCommandActivities, observeCommandActivity } from "../../chat/tool/command-activity-live.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { denScrollDebugLog } from "../../chat/stream/den-scroll-debug.ts";
import { disclosureToggle, isDisclosureOpen } from "../../test/transcript-disclosure-fixture.ts";
import { ToolPartShell } from "./ToolPartShell.tsx";
import { CommandActivitySpan } from "./CommandActivitySpan.tsx";
import { ToolPartCard } from "./ToolPartCard.tsx";
import { activityDecision, activityFact, activityProgram, activityTool } from "../../test/command-activity-fixtures.ts";
import { registerOpenFilesSurfaceSink } from "../../platform/navigation/open-files-surface.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { saveVerboseMode } from "../../settings/system/debug-prefs.ts";
vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: vi.fn() }));
vi.mock("../../chat/stream/den-scroll-debug.ts", () => ({
  isStreamScrollDebugEnabled: () => true,
  denScrollDebugLog: vi.fn(),
}));

function entry(
  id: string,
  group: string,
  state: "running" | "completed" | "failed" = "running",
  phase?: CommandActivity["phase"],
): ActivitySpanEntry {
  const label = phase === "execute" ? "Run process" : "Read source";
  const outcome = { running: "Running", completed: "Completed", failed: "Failed" }[state];
  const fact: CommandActivity = { id, group, label, subject: "source.rs", state, outcome, phase, execution_ids: [id], spans: [] };
  const program = { id, state, activities: [fact] } as CommandProgramReceipt;
  const status = state === "failed" ? "error" : state;
  return { kind: "tool", part: { id, toolCallId: id, assistantMessageId: "assistant", messageId: "result", tool: "command", kind: "command", status, commandProgram: program } };
}

it("preserves group and row elements and expansion while later work arrives", async () => {
  const [entries, setEntries] = createSignal([entry("read", "Exploring project files")]);
  const view = render(() => <CommandActivitySpan label="Running commands" entries={entries()} layout="chat" entryKey="command-groups-stability" renderToolEntry={part => <ToolPartShell part={part()} layout="chat"><span>Recorded detail</span></ToolPartShell>} />);
  const group = view.container.querySelector('[data-testid="activity-span-card"]')!;
  fireEvent.click(disclosureToggle(group));
  const row = group.querySelector('[data-testid="tool-part-card"]')!;
  await waitFor(() => expect(isDisclosureOpen(row)).toBe(true));
  setEntries([entry("read", "Exploring project files", "completed"), entry("build", "Running programs")]);
  expect(view.container.querySelectorAll('[data-testid="activity-span-card"]')).toHaveLength(1);
  expect(view.container.querySelector('[data-testid="activity-span-card"]')).toBe(group);
  expect(group.querySelector('[data-testid="tool-part-card"]')).toBe(row);
  expect(isDisclosureOpen(row)).toBe(true);
  expect(group.getAttribute("data-status")).toBe("running");
  expect(group.querySelector(".den-activity-span-name")?.textContent).toBe("Exploring project files");
});

it("advances the span label only on a strict rank increase (AT-ACT-HEAD-01)", () => {
  const [entries, setEntries] = createSignal([entry("read", "Reading files", "completed", "inspect")]);
  const view = render(() => <CommandActivitySpan label="Running commands" entries={entries()} layout="chat" entryKey="command-groups-rank" renderToolEntry={part => <ToolPartShell part={part()} layout="chat"><span>Recorded detail</span></ToolPartShell>} />);
  const label = () => view.container.querySelector(".den-activity-span-name")?.textContent;
  const sequence = () => view.container.querySelector(".den-activity-span-hint")?.textContent;
  expect(sequence()).toBe("");
  expect(label()).toBe("Reading files");
  setEntries([entry("read", "Reading files", "completed", "inspect"), entry("search", "Searching codebase", "completed", "inspect")]);
  expect(sequence()).toBe("Reading files → Searching codebase");
  expect(label()).toBe("Reading files");
  setEntries([entry("read", "Reading files", "completed", "inspect"), entry("search", "Searching codebase", "completed", "inspect"), entry("map", "Mapping codebase", "completed", "inspect")]);
  expect(label()).toBe("Mapping codebase");
  expect(sequence()).toBe("Reading files → Searching codebase → Mapping codebase");
  setEntries(previous => [...previous, entry("again", "Reading files"), entry("build", "Running programs"), entry("test", "Running tests")]);
  expect(sequence()).toBe("Reading files → Searching codebase → Mapping codebase · +2");
});

it("takes its status from the settled receipt, not the program recorded while running", async () => {
  const recorded = entry("server", "Running programs");
  const settled = { ...(recorded.kind === "tool" ? recorded.part.commandProgram! : ({} as CommandProgramReceipt)), state: "completed" } as CommandProgramReceipt;
  const fact = { ...settled.activities![0]!, state: "completed", outcome: "Completed", steps: [] } as CommandActivity;
  vi.mocked(getLycaonClient).mockReturnValue(stubClient({ getCommandProgramReceipt: vi.fn().mockResolvedValue({ program: settled, activities: [fact], executions: [], nodes: [] }) }));
  const view = render(() => <CommandActivitySpan label="Running commands" entries={[recorded]} sessionId="session" layout="chat" entryKey="command-groups-settled" renderToolEntry={part => <ToolPartShell part={part()} layout="chat"><span>Recorded detail</span></ToolPartShell>} />);
  const group = () => view.container.querySelector('[data-testid="activity-span-card"]');
  await waitFor(() => expect(group()?.getAttribute("data-status")).toBe("done"));
});

it.each([
  { group: "Running programs", sequence: "" },
  { group: "Running tests", sequence: "Running programs → Running tests" },
])("updates recovered command chrome with $group and keeps its rows", async ({ group, sequence }) => {
  const attempts = Array.from({ length: 16 }, (_, index) =>
    entry(`attempt-${index}`, "Running programs", "failed", "execute"),
  );
  const [entries, setEntries] = createSignal(attempts);
  const { container } = render(() => (
    <CommandActivitySpan
      label="Running commands"
      layout="chat"
      entries={entries()}
      renderToolEntry={(part) => <ToolPartShell part={part()} layout="chat"><span>Recorded detail</span></ToolPartShell>}
    />
  ));
  const span = container.querySelector('[data-testid="activity-span-card"]')!;
  const toggle = disclosureToggle(span);
  const failureChip = () => span.querySelector('[data-testid="activity-span-failed"]');
  const headerSequence = () => span.querySelector(".den-activity-span-hint")?.textContent;
  const rows = () => Array.from(span.querySelectorAll('[data-testid="tool-part-card"]'));
  expect(span.getAttribute("data-status")).toBe("error");
  expect(failureChip()?.textContent).toBe("16 failed");
  expect(toggle.getAttribute("aria-label")).toContain("16 failed");
  expect(headerSequence()).toBe("");

  fireEvent.click(toggle);
  const failedRows = rows();
  expect(failedRows).toHaveLength(16);
  setEntries([...attempts, entry("verification", group, "completed", "execute")]);
  expect(span.getAttribute("data-status")).toBe("done");
  expect(failureChip()).toBeNull();
  expect(toggle.getAttribute("aria-label")).toBe("Running programs, done, 17 actions");
  expect(headerSequence()).toBe(sequence);
  const recoveredRows = rows();
  expect(recoveredRows).toHaveLength(17);
  failedRows.forEach((row, index) => {
    expect(recoveredRows[index]).toBe(row);
    expect(row.getAttribute("data-status")).toBe("error");
  });
  expect(recoveredRows.at(-1)?.getAttribute("data-status")).toBe("done");

  try {
    await saveVerboseMode(true);
    expect(failureChip()?.textContent).toBe("16 failed");
    expect(toggle.getAttribute("aria-label")).toContain("16 failed");
    expect(headerSequence()).toBe(sequence);
    expect(rows()).toHaveLength(17);
    expect(isDisclosureOpen(span)).toBe(true);
  } finally {
    await saveVerboseMode(false);
  }
});

it.each([true, false])("opens an attached decision and every folded program receipt in Files (host link: %s)", async hostLink => {
  const sink = vi.fn();
  const unregister = registerOpenFilesSurfaceSink(sink);
  const read = (id: string) => activityTool(id, "command", { commandProgram: activityProgram(id, {
    activities: [activityFact(id, {
      action: {
        label: "Read files", status: "done", ran: { text: id, noun: "command" },
        facts: [{ kind: "steps", count: 1, sequence: [id] }], row: ["steps"],
        links: hostLink ? [{ kind: "receipt", receipts: [{ program_id: `program-${id}`, activity_id: id }] }] : [],
      },
    })],
  }) });
  try {
    const view = render(() => <CommandActivitySpan
      label="Reading files" entries={[activityDecision("turn-decision"), read("first"), read("next")]}
      layout="chat" entryKey={`command-record-links-${hostLink}`}
      renderToolEntry={part => <ToolPartCard part={part()} layout="chat" projectId="project" slots={{}} />}
    />);
    const group = view.getByTestId("activity-span-card");
    fireEvent.click(disclosureToggle(group));
    await waitFor(() => expect(isDisclosureOpen(view.getByTestId("action-card"))).toBe(true));
    fireEvent.click(view.getByTestId("command-activity-decisions"));
    expect(sink).toHaveBeenLastCalledWith(expect.objectContaining({ document: expect.objectContaining({
      content: { kind: "inline", text: expect.any(String) },
    }) }));
    fireEvent.click(view.getByTestId("action-link-receipt"));
    expect(sink).toHaveBeenLastCalledWith(expect.objectContaining({ document: expect.objectContaining({
      content: { kind: "command_receipts", scopes: [
        { programId: "program-first", activityId: "first" }, { programId: "program-next", activityId: "next" },
      ] },
    }) }));
  } finally {
    unregister();
  }
});

it("keeps independently visible rows when a session's span remounts under a new key", () => {
  const inspect = (id: string) => activityTool(id, "command", { commandProgram: activityProgram(id, {
    activities: [activityFact(id, { steps: [] })],
  }) });
  const first = inspect("first");
  const second = inspect("second");
  const pending = activityTool("first", "command", { status: "running" });
  const span = (entries: () => readonly ActivitySpanEntry[], key: string) => (
    <CommandActivitySpan label="Reading files" entries={entries()} layout="chat"
      sessionId="continuity-remount" entryKey={key}
      renderToolEntry={part => <ToolPartShell part={part()} layout="chat"><span>Recorded detail</span></ToolPartShell>} />
  );
  const [entries, setEntries] = createSignal<readonly ActivitySpanEntry[]>([pending, second]);
  const original = render(() => span(entries, "original"));
  const rowKeys = (container: HTMLElement) => [...container.querySelectorAll("[data-activity-entry-key]")]
    .map(row => row.getAttribute("data-activity-entry-key"));
  fireEvent.click(disclosureToggle(original.getByTestId("activity-span-card")));
  expect(rowKeys(original.container)).toEqual(["second"]);
  setEntries([first, second]);
  expect(rowKeys(original.container)).toEqual(["second", "first"]);
  original.unmount();
  const remount = render(() => span(() => [first, second], "remount"));
  fireEvent.click(disclosureToggle(remount.getByTestId("activity-span-card")));
  expect(rowKeys(remount.container)).toEqual(["second", "first"]);
});

it("maintains read-row continuity, per-member Files receipts, and zero withdrawals across live handoff and remount", async () => {
  vi.mocked(denScrollDebugLog).mockClear();
  const sink = vi.fn();
  const unregister = registerOpenFilesSurfaceSink(sink);

  const makeReadFact = (activityId: string, programId: string, state: CommandActivity["state"] = "completed"): CommandActivity =>
    activityFact(activityId, {
      phase: "inspect",
      state,
      outcome: state === "running" ? "Running" : "Completed",
      steps: [],
      action: {
        label: "Read files",
        status: state === "running" ? "running" : "done",
        ran: { text: activityId, noun: "command" },
        facts: [{ kind: "steps", count: 1, sequence: [activityId] }],
        row: ["steps"],
        links: [{ kind: "receipt", receipts: [{ program_id: programId, activity_id: activityId }] }],
      },
    });

  const settledRead = (partId: string, callId: string, programId: string, activityId: string): ActivitySpanEntry =>
    activityTool(partId, "command", {
      toolCallId: callId,
      commandProgram: activityProgram(programId, {
        id: programId,
        state: "completed",
        activities: [makeReadFact(activityId, programId, "completed")],
      }),
    });

  const pendingRead = (partId: string, callId: string): ActivitySpanEntry =>
    activityTool(partId, "command", { toolCallId: callId, status: "running" });

  const rowKeys = (container: HTMLElement) =>
    [...container.querySelectorAll("[data-activity-entry-key]")].map(el => el.getAttribute("data-activity-entry-key"));

  const sessionA = "gs05-integrated-handoff";
  const sessionOutOfOrder = "gs05-out-of-order-handoff";
  const readA = settledRead("assistant:call-a", "call-a", "program-read-a", "act-read-a");
  const liveB = pendingRead("assistant:call-b", "call-b");
  const readB = settledRead("assistant:call-b", "call-b", "program-read-b", "act-read-b");

  try {
    const renderedAccessors = new Map<string, () => ToolPartView>();
    let leadMountCount = 0;
    const [entries, setEntries] = createSignal<readonly ActivitySpanEntry[]>([readA]);
    const mountSpan = (key: string) =>
      render(() => (
        <CommandActivitySpan
          label="Reading files"
          entries={entries()}
          layout="chat"
          sessionId={sessionA}
          entryKey={key}
          renderToolEntry={part => {
            const id = part().id;
            if (id === "assistant:call-a") leadMountCount += 1;
            renderedAccessors.set(id, part);
            return <ToolPartCard part={part()} layout="chat" projectId="project" slots={{}} />;
          }}
        />
      ));

    // Step 1: A settled.
    const view = mountSpan("handoff-initial");
    const group = view.getByTestId("activity-span-card");
    fireEvent.click(disclosureToggle(group));
    expect(rowKeys(view.container)).toEqual(["assistant:call-a"]);
    expect(renderedAccessors.get("assistant:call-a")?.().commandActivity?.calls.map(c => c.id)).toEqual([
      "assistant:call-a",
    ]);
    expect(group.getAttribute("data-status")).toBe("done");

    // Step 2: B arrives live with matching program and activity IDs; lead row persists and holds [A, B] immediately.
    observeCommandActivity({
      session_id: sessionA,
      tool_call_id: "call-b",
      tool_name: "command",
      activity_id: "lease-b",
      kind: "running_tool",
      status: "active",
      started_at: "2026-11-23T00:00:00Z",
      progress: {
        phase: "executing",
        files_searched: 0,
        files_skipped: 0,
        matches: 0,
        command_program: activityProgram("program-read-b", {
          id: "program-read-b",
          state: "running",
          activities: [makeReadFact("act-read-b", "program-read-b", "running")],
        }),
      },
    });
    setEntries([readA, liveB]);
    expect(rowKeys(view.container)).toEqual(["assistant:call-a"]);
    expect(leadMountCount).toBe(1);
    expect(renderedAccessors.get("assistant:call-a")?.().commandActivity?.calls.map(c => c.id)).toEqual([
      "assistant:call-a",
      "assistant:call-b",
    ]);
    expect(group.getAttribute("data-status")).toBe("running");

    // Step 3: Clear live progress and deliver B settled with the same program and activity IDs.
    clearCommandActivities(sessionA);
    setEntries([readA, readB]);
    expect(rowKeys(view.container)).toEqual(["assistant:call-a"]);
    expect(leadMountCount).toBe(1);
    expect(renderedAccessors.get("assistant:call-a")?.().commandActivity?.calls.map(c => c.id)).toEqual([
      "assistant:call-a",
      "assistant:call-b",
    ]);
    expect(group.getAttribute("data-status")).toBe("done");

    // Remount in the same session after clearing live progress and open Files for the folded receipts.
    view.unmount();
    const remounted = mountSpan("handoff-remount");
    fireEvent.click(disclosureToggle(remounted.getByTestId("activity-span-card")));
    await waitFor(() => expect(isDisclosureOpen(remounted.getByTestId("action-card"))).toBe(true));
    expect(rowKeys(remounted.container)).toEqual(["assistant:call-a"]);
    fireEvent.click(remounted.getByTestId("action-link-receipt"));
    expect(sink).toHaveBeenLastCalledWith(
      expect.objectContaining({
        document: expect.objectContaining({
          content: {
            kind: "command_receipts",
            scopes: [
              { programId: "program-read-a", activityId: "act-read-a" },
              { programId: "program-read-b", activityId: "act-read-b" },
            ],
          },
        }),
      }),
    );
    remounted.unmount();

    // Out-of-order case: B is already independently visible while A is pending;
    // B keeps its row and each opens its own receipt in Files.
    const [outEntries, setOutEntries] = createSignal<readonly ActivitySpanEntry[]>([
      pendingRead("assistant:call-a", "call-a"),
      readB,
    ]);
    const mountOut = (key: string) =>
      render(() => (
        <CommandActivitySpan
          label="Reading files"
          entries={outEntries()}
          layout="chat"
          sessionId={sessionOutOfOrder}
          entryKey={key}
          renderToolEntry={part => <ToolPartCard part={part()} layout="chat" projectId="project" slots={{}} />}
        />
      ));
    const outView = mountOut("out-initial");
    fireEvent.click(disclosureToggle(outView.getByTestId("activity-span-card")));
    expect(rowKeys(outView.container)).toEqual(["assistant:call-b"]);

    setOutEntries([readA, readB]);
    clearCommandActivities(sessionOutOfOrder);
    expect(rowKeys(outView.container)).toEqual(["assistant:call-b", "assistant:call-a"]);

    outView.unmount();
    const outRemount = mountOut("out-remount");
    fireEvent.click(disclosureToggle(outRemount.getByTestId("activity-span-card")));
    expect(rowKeys(outRemount.container)).toEqual(["assistant:call-b", "assistant:call-a"]);

    const cards = outRemount.getAllByTestId("action-card");
    expect(cards).toHaveLength(2);

    if (!isDisclosureOpen(cards[0]!)) {
      fireEvent.click(disclosureToggle(cards[0]!));
    }
    const firstReceiptBtn = await waitFor(() => within(cards[0]!).getByTestId("action-link-receipt"));
    fireEvent.click(firstReceiptBtn);
    expect(sink).toHaveBeenLastCalledWith(
      expect.objectContaining({
        document: expect.objectContaining({
          content: { kind: "command_receipts", scopes: [{ programId: "program-read-b", activityId: "act-read-b" }] },
        }),
      }),
    );

    if (!isDisclosureOpen(cards[1]!)) {
      fireEvent.click(disclosureToggle(cards[1]!));
    }
    const secondReceiptBtn = await waitFor(() => within(cards[1]!).getByTestId("action-link-receipt"));
    fireEvent.click(secondReceiptBtn);
    expect(sink).toHaveBeenLastCalledWith(
      expect.objectContaining({
        document: expect.objectContaining({
          content: { kind: "command_receipts", scopes: [{ programId: "program-read-a", activityId: "act-read-a" }] },
        }),
      }),
    );
    outRemount.unmount();

    const withdrawals = vi
      .mocked(denScrollDebugLog)
      .mock.calls.filter(([domain, event]) => domain === "scroll" && event === "row-withdrawn");
    expect(withdrawals).toEqual([]);
  } finally {
    clearCommandActivities(sessionA);
    clearCommandActivities(sessionOutOfOrder);
    unregister();
  }
});
