import { createSignal } from "solid-js";
import { expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import type { CommandActivity, CommandProgramReceipt } from "../../api/types.ts";
import type { ActivitySpanEntry } from "../../chat/transcript/projection/transcript-item-model.ts";
import { disclosureToggle, isDisclosureOpen } from "../../test/transcript-disclosure-fixture.ts";
import { ToolPartShell } from "./ToolPartShell.tsx";
import { CommandActivitySpan } from "./CommandActivitySpan.tsx";
import { ToolPartCard } from "./ToolPartCard.tsx";
import { activityDecision, activityFact, activityProgram, activityTool } from "../../test/command-activity-fixtures.ts";
import { registerOpenFilesSurfaceSink } from "../../platform/navigation/open-files-surface.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: vi.fn() }));

function entry(id: string, group: string, state: "running" | "completed" = "running", phase?: CommandActivity["phase"]): ActivitySpanEntry {
  const fact: CommandActivity = { id, group, label: "Read source", subject: "source.rs", state, outcome: state === "running" ? "Running" : "Completed", phase, execution_ids: [id], spans: [] };
  const program = { id, state, activities: [fact] } as CommandProgramReceipt;
  return { kind: "tool", part: { id, toolCallId: id, assistantMessageId: "assistant", messageId: "result", tool: "command", kind: "command", status: state === "running" ? "running" : "completed", commandProgram: program } };
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
  expect(label()).toBe("Reading files");
  setEntries([entry("read", "Reading files", "completed", "inspect"), entry("search", "Searching codebase", "completed", "inspect")]);
  expect(label()).toBe("Reading files");
  setEntries([entry("read", "Reading files", "completed", "inspect"), entry("search", "Searching codebase", "completed", "inspect"), entry("map", "Mapping codebase", "completed", "inspect")]);
  expect(label()).toBe("Mapping codebase");
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

it("opens an attached decision and every folded program receipt in Files", async () => {
  const sink = vi.fn();
  const unregister = registerOpenFilesSurfaceSink(sink);
  const read = (id: string) => activityTool(id, "command", { commandProgram: activityProgram(id, {
    activities: [activityFact(id, {
      action: {
        label: "Read files", status: "done", ran: { text: id, noun: "command" },
        facts: [{ kind: "steps", count: 1, sequence: [id] }], row: ["steps"],
        links: [{ kind: "receipt", receipts: [{ program_id: `program-${id}`, activity_id: id }] }],
      },
    })],
  }) });
  try {
    const view = render(() => <CommandActivitySpan
      label="Reading files" entries={[activityDecision("turn-decision"), read("first"), read("next")]}
      layout="chat" entryKey="command-record-links"
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
