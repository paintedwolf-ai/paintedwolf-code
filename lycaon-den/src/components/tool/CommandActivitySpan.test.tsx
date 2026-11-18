import { createSignal } from "solid-js";
import { expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import type { CommandActivity, CommandProgramReceipt } from "../../api/types.ts";
import type { ActivitySpanEntry } from "../../chat/transcript/projection/transcript-item-model.ts";
import { disclosureToggle, isDisclosureOpen } from "../../test/transcript-disclosure-fixture.ts";
import { ToolPartShell } from "./ToolPartShell.tsx";
import { CommandActivitySpan } from "./CommandActivitySpan.tsx";
import { stubClient } from "../../test/client-fixture.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { saveVerboseMode } from "../../settings/system/debug-prefs.ts";
vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: vi.fn() }));

function entry(
  id: string,
  group: string,
  state: "running" | "completed" | "failed" = "running",
  phase?: CommandActivity["phase"],
  subject = "source.rs",
): ActivitySpanEntry {
  const label = phase === "execute" ? "Run process" : "Read source";
  const outcome = { running: "Running", completed: "Completed", failed: "Failed" }[state];
  const fact: CommandActivity = { id, group, label, subject, state, outcome, phase, execution_ids: [id], spans: [] };
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

it("updates a recovered command span's header without withdrawing its failed runs", async () => {
  const attempts = Array.from({ length: 16 }, (_, index) =>
    entry(`attempt-${index}`, "Running programs", "failed", "execute", "Run test probe"),
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
  const hint = () => span.querySelector(".den-activity-span-hint")?.textContent;
  const rows = () => Array.from(span.querySelectorAll('[data-testid="tool-part-card"]'));
  expect(span.getAttribute("data-status")).toBe("error");
  expect(failureChip()?.textContent).toBe("16 failed");
  expect(toggle.getAttribute("aria-label")).toContain("16 failed");
  expect(hint()).toBe("Run test probe");

  fireEvent.click(toggle);
  const failedRows = rows();
  expect(failedRows).toHaveLength(16);
  setEntries([...attempts, entry("verification", "Running programs", "completed", "execute", "Verify the fix")]);
  expect(span.getAttribute("data-status")).toBe("done");
  expect(failureChip()).toBeNull();
  expect(toggle.getAttribute("aria-label")).toBe("Running programs, done, 17 actions");
  expect(hint()).toBe("Verify the fix, +1");
  expect(rows()).toHaveLength(17);
  failedRows.forEach((row, index) => {
    expect(rows()[index]).toBe(row);
    expect(row.getAttribute("data-status")).toBe("error");
  });
  expect(rows().at(-1)?.getAttribute("data-status")).toBe("done");

  try {
    await saveVerboseMode(true);
    expect(failureChip()?.textContent).toBe("16 failed");
    expect(toggle.getAttribute("aria-label")).toContain("16 failed");
    expect(hint()).toBe("Run test probe, +1");
    expect(rows()).toHaveLength(17);
    expect(isDisclosureOpen(span)).toBe(true);
  } finally {
    await saveVerboseMode(false);
  }
});
