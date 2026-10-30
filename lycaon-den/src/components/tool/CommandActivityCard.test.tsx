import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import type { CommandActivity, CommandExecutionReceipt, CommandProgramReceipt } from "../../api/types.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { disclosureToggle } from "../../test/transcript-disclosure-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { clearTranscriptEntryMemory } from "../../chat/transcript/presentation/transcript-entry.ts";
import { CommandActivityCard } from "./CommandActivityCard.tsx";
import { writeClipboardText } from "../../utils/clipboard.ts";
vi.mock("../../utils/clipboard.ts", () => ({ writeClipboardText: vi.fn().mockResolvedValue(undefined) }));

const source = "cd /project && cargo build --release | tail -2 && cargo test | head -3";
const program: CommandProgramReceipt = { id: "program", parent_receipt_id: "receipt", language_version: 1, catalog_digest: "catalog", source_digest: "digest", source_ref: "source", state: "completed", progress_state: "unchanged", detail_ref: "detail", started_at: "2026-10-30T10:00:00Z" };
const fact: CommandActivity = { id: "test", group: "Running programs", label: "Run process", subject: "cargo test", state: "completed", outcome: "Exited 0", status_code: 0, execution_ids: ["test"], spans: [{ source_ref: "source", start_byte: source.indexOf("cargo test"), end_byte: source.length }] };
function part(): ToolPartView {
 const original: ToolPartView = { id: "assistant:call", assistantMessageId: "assistant", messageId: "result", toolCallId: "call", tool: "command", kind: "command", status: "completed", args: { command: source }, commandProgram: program };
 return { ...original, commandActivity: { fact, members: [fact], calls: [original] } };
}
function receipt(): CommandExecutionReceipt {
 return { id: "test", program_id: "program", node_id: "node", parent_execution_id: "pipeline", owner: "processes", lifecycle: "effect_attempt", definition_digest: "definition", arguments_digest: "args", arguments_ref: "args", command: "cargo test", state: "completed", owner_started: true, owner_invoked: true, effect_certainty: "attempted", complete: true, publication_eligible: false, output_publishable: true, detail_ref: "detail", status_code: 0, outputs: [{ stream: "stdout", ref: "out", size_bytes: 12, complete: true, redacted: false, preview: "test output", preview_truncated: false }] };
}
beforeEach(() => { clearTranscriptEntryMemory("activity-card-test"); vi.clearAllMocks(); });

describe("compact command activity details", () => {
 it("loads engineer details only after opening the existing card", async () => {
  const get = vi.fn().mockResolvedValue({ program, executions: [receipt()], nodes: [], executions_omitted: 0, nodes_omitted: 0 });
  const view = render(() => <CommandActivityCard part={part()} layout="chat" sessionId="activity-card-test" client={stubClient({ getCommandProgramReceipt: get })} />);
  expect(get).not.toHaveBeenCalled();
  fireEvent.click(disclosureToggle(view.container.querySelector('[data-testid="tool-part-card"]')));
  await waitFor(() => expect(get).toHaveBeenCalledOnce());
  expect(view.container.querySelector('[data-testid="command-activity-details"]')).toBeTruthy();
  expect(view.container.querySelector("mark")?.textContent).toContain("cargo test");
  expect(view.container.querySelectorAll("details")).toHaveLength(0);
  expect(view.container.textContent).toContain("test output");
  fireEvent.click(view.getByRole("button", { name: "Copy command" }));
  await waitFor(() => expect(writeClipboardText).toHaveBeenCalledWith(source));
 });
 it("keeps long source and output behind bounded previews and full-content links", async () => {
  const value = part(); value.commandActivity!.calls[0]!.args = { command: "x".repeat(20000) };
  const execution = receipt(); execution.outputs![0]!.preview = "y".repeat(10000);
  const get = vi.fn().mockResolvedValue({ program, executions: [execution], nodes: [], executions_omitted: 0, nodes_omitted: 0 });
  const view = render(() => <CommandActivityCard part={value} layout="chat" sessionId="activity-card-test" client={stubClient({ getCommandProgramReceipt: get })} />);
  fireEvent.click(disclosureToggle(view.container.querySelector('[data-testid="tool-part-card"]')));
  await waitFor(() => expect(view.container.querySelector(".den-command-program-output-preview")).toBeTruthy());
  expect(view.container.querySelector(".den-command-program-output-preview")!.textContent!.length).toBeLessThanOrEqual(480);
  expect(view.container.querySelector(".den-command-activity-command")!.textContent!.length).toBeLessThanOrEqual(481);
  expect(view.container.textContent).toContain("Full original command");
 });
 it("keeps the recorded row when detail loading fails", async () => {
  const view = render(() => <CommandActivityCard part={part()} layout="chat" sessionId="activity-card-test" client={stubClient({ getCommandProgramReceipt: vi.fn().mockRejectedValue(new Error("offline")) })} />);
  fireEvent.click(disclosureToggle(view.container.querySelector('[data-testid="tool-part-card"]')));
  await waitFor(() => expect(view.container.textContent).toContain("Execution details are unavailable"));
  expect(view.container.textContent).toContain("cargo test");
 });
});
