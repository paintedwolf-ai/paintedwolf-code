import { createSignal } from "solid-js";
import { expect, it } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import type { CommandActivity, CommandProgramReceipt } from "../../api/types.ts";
import type { ActivitySpanEntry } from "../../chat/transcript/projection/transcript-item-model.ts";
import { disclosureToggle, isDisclosureOpen } from "../../test/transcript-disclosure-fixture.ts";
import { ToolPartShell } from "./ToolPartShell.tsx";
import { CommandActivitySpan } from "./CommandActivitySpan.tsx";

function entry(id: string, group: string, state: "running" | "completed" = "running"): ActivitySpanEntry {
  const fact: CommandActivity = { id, group, label: "Read source", subject: "source.rs", state, outcome: state === "running" ? "Running" : "Completed", execution_ids: [id], spans: [] };
  const program = { id, state, activities: [fact] } as CommandProgramReceipt;
  return { kind: "tool", part: { id, toolCallId: id, assistantMessageId: "assistant", messageId: "result", tool: "command", kind: "command", status: state === "running" ? "running" : "completed", commandProgram: program } };
}

it("preserves group and row elements and expansion while later work arrives", () => {
  const [entries, setEntries] = createSignal([entry("read", "Exploring project files")]);
  const view = render(() => <CommandActivitySpan label="Running commands" entries={entries()} layout="chat" entryKey="command-groups-stability" renderToolEntry={part => <ToolPartShell part={part()} layout="chat"><span>Recorded detail</span></ToolPartShell>} />);
  const group = view.container.querySelector('[data-testid="activity-span-card"]')!;
  fireEvent.click(disclosureToggle(group));
  const row = group.querySelector('[data-testid="tool-part-card"]')!;
  expect(isDisclosureOpen(row)).toBe(true);
  setEntries([entry("read", "Exploring project files", "completed"), entry("build", "Running programs")]);
  expect(view.container.querySelectorAll('[data-testid="activity-span-card"]')).toHaveLength(1);
  expect(view.container.querySelector('[data-testid="activity-span-card"]')).toBe(group);
  expect(group.querySelector('[data-testid="tool-part-card"]')).toBe(row);
  expect(isDisclosureOpen(row)).toBe(true);
  expect(group.getAttribute("data-status")).toBe("running");
  expect(group.querySelector(".den-activity-span-name")?.textContent).toBe("Running programs");
});
