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
const fact: CommandActivity = { id: "test", group: "Running programs", phase: "execute", label: "Run process", subject: "cargo test", state: "completed", outcome: "Exited 0", status_code: 0, execution_ids: ["test"], spans: [{ source_ref: "source", start_byte: source.indexOf("cargo test"), end_byte: source.length }] };
function part(): ToolPartView {
 const original: ToolPartView = { id: "assistant:call", assistantMessageId: "assistant", messageId: "result", toolCallId: "call", tool: "command", kind: "command", status: "completed", args: { command: source }, commandProgram: program };
 return { ...original, commandActivity: { fact, members: [fact], calls: [original], programIds: [program.id] } };
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
  expect(get).toHaveBeenCalledWith("activity-card-test", "program", {limit:6,cursor:undefined,activity_id:"test"});
  expect(view.container.querySelector('[data-testid="command-activity-details"]')).toBeTruthy();
  expect(view.container.querySelector("pre")).toBeNull();
  expect(view.container.querySelectorAll("details")).toHaveLength(0);
  await waitFor(() => expect(view.container.textContent).toContain("Execution receipts and diagnostics"));
  const status = view.container.querySelector('[role="status"]');
  expect(status?.hasAttribute("hidden")).toBe(true);
  fireEvent.click(view.getByRole("button", { name: "Copy command" }));
  await waitFor(() => expect(writeClipboardText).toHaveBeenCalledWith(source));
  await waitFor(() => expect(view.getByRole("status").textContent).toBe("Copied"));
  expect(view.getByRole("status")).toBe(status);
 });
 it("keeps long source and output behind Files links", async () => {
  const value = part(); value.commandActivity!.calls[0]!.args = { command: "x".repeat(20000) };
  const execution = receipt(); execution.outputs![0]!.preview = "y".repeat(10000);
  const get = vi.fn().mockResolvedValue({ program, executions: [execution], nodes: [], executions_omitted: 0, nodes_omitted: 0 });
  const view = render(() => <CommandActivityCard part={value} layout="chat" sessionId="activity-card-test" client={stubClient({ getCommandProgramReceipt: get })} />);
  fireEvent.click(disclosureToggle(view.container.querySelector('[data-testid="tool-part-card"]')));
  await waitFor(() => expect(view.container.textContent).toContain("Execution receipts and diagnostics"));
  expect(view.container.querySelector("pre")).toBeNull();
  expect(view.container.textContent).not.toContain("y".repeat(100));
  expect(view.container.textContent).not.toContain("x".repeat(100));
 });
 it("keeps the recorded row when detail loading fails", async () => {
  const view = render(() => <CommandActivityCard part={part()} layout="chat" sessionId="activity-card-test" client={stubClient({ getCommandProgramReceipt: vi.fn().mockRejectedValue(new Error("offline")) })} />);
  fireEvent.click(disclosureToggle(view.container.querySelector('[data-testid="tool-part-card"]')));
  await waitFor(() => expect(view.container.textContent).toContain("Execution details are unavailable"));
  expect(view.container.textContent).toContain("cargo test");
 });
});

it("does not reload unchanged receipts when live snapshots replace their objects", async () => {
 const { createSignal } = await import("solid-js");
 const get=vi.fn().mockResolvedValue({ program, executions:[receipt()],nodes:[],executions_omitted:0,nodes_omitted:0 });
 const [value,setValue]=createSignal(part());
 const client=stubClient({ getCommandProgramReceipt:get });
 const view=render(()=><CommandActivityCard part={value()} layout="chat" sessionId="activity-card-test" client={client}/>);
 fireEvent.click(disclosureToggle(view.container.querySelector('[data-testid="tool-part-card"]')));
 await waitFor(()=>expect(view.container.textContent).toContain("Execution receipts and diagnostics"));
 const copy=view.getByRole("button",{name:"Copy command"});
 const next=part(); next.commandActivity!.fact={...next.commandActivity!.fact};
 setValue(next);
 await Promise.resolve();
 expect(get).toHaveBeenCalledOnce();
 expect(view.getByRole("button",{name:"Copy command"})).toBe(copy);
});

it("preserves uncertain effects, approvals and unpublished changes in compact details", async () => {
 const { ToolPartCard }=await import("./ToolPartCard.tsx");
 const value=part().commandActivity!.calls[0]!;
 const execution=receipt();
 execution.effect_certainty="unknown"; execution.approval_ref="approval-1"; execution.accepted_revision="revision-1"; execution.held_version_id="held-1"; execution.recovery_ref="recovery-1";
 const get=vi.fn().mockResolvedValue({ program:{...program,state:"waiting"},executions:[execution],nodes:[],executions_omitted:0,nodes_omitted:0 });
 const view=render(()=><ToolPartCard part={value} layout="chat" sessionId="activity-card-test" client={stubClient({getCommandProgramReceipt:get})}/>);
 fireEvent.click(disclosureToggle(view.container.querySelector('[data-testid="tool-part-card"]')));
 await waitFor(()=>expect(view.container.textContent).toContain("Unknown outcome"));
 expect(view.container.textContent).toContain("Execution receipts and diagnostics");
 expect(view.container.textContent).not.toContain("approval-1");
 expect(view.container.querySelectorAll("details")).toHaveLength(0);
 expect(view.container.querySelectorAll('[data-testid="tool-part-card"]')).toHaveLength(1);
});

it("opens the complete activity records in Files without record controls", async () => {
 const { registerOpenFilesSurfaceSink, resetOpenFilesSurfaceForTests } = await import("../../platform/navigation/open-files-surface.ts");
 const { onTestFinished } = await import("vitest");
 onTestFinished(resetOpenFilesSurfaceForTests);
 const sink = vi.fn(); registerOpenFilesSurfaceSink(sink);
 const get = vi.fn().mockResolvedValue({ program, executions: [receipt()], nodes: [], executions_omitted: 10704, next_cursor: "next", nodes_omitted: 0 });
 const view = render(() => <CommandActivityCard part={part()} layout="chat" sessionId="activity-card-test" projectId="project" client={stubClient({getCommandProgramReceipt:get})} />);
 fireEvent.click(disclosureToggle(view.container.querySelector('[data-testid="tool-part-card"]')));
 await waitFor(() => expect(view.getByRole("button", {name:/Execution receipts/})).toBeTruthy());
 expect(view.queryByRole("button", {name:/Previous records|Next records|Whole command|This activity/})).toBeNull();
 fireEvent.click(view.getByRole("button", {name:/Execution receipts/}));
 expect(sink.mock.calls.at(-1)?.[0].document.content).toMatchObject({kind:"command_receipts",programs:[{id:"program",activityId:"test"}]});
 expect(get).toHaveBeenCalledOnce();
});

it("reveals a folded call on a later detail page through Find", async () => {
 const { createSignal }=await import("solid-js");
 const { bindFindableView }=await import("../../find/use-findable-view.ts");
 const { openFind,setFindQuery,findNext,findController,resetFindControllerForTests }=await import("../../find/find-controller.ts");
 const { registerOpenFilesSurfaceSink,resetOpenFilesSurfaceForTests }=await import("../../platform/navigation/open-files-surface.ts");
 const { onTestFinished }=await import("vitest");
 onTestFinished(()=>{resetFindControllerForTests();resetOpenFilesSurfaceForTests();});
 const sink=vi.fn();registerOpenFilesSurfaceSink(sink);
 const value=part();
 value.commandActivity!.calls=Array.from({length:5},(_,index)=>({...value.commandActivity!.calls[0]!,id:`source-${index}`,toolCallId:`source-${index}`,args:{command:index===4?"UNIQUE_FOLDED_NEEDLE":"other command"}}));
 const client=stubClient({getCommandProgramReceipt:vi.fn().mockResolvedValue({program,executions:[receipt()],nodes:[],executions_omitted:0,nodes_omitted:0})});
 const view=render(()=>{
  const [host,setHost]=createSignal<HTMLElement>();
  bindFindableView({id:"session-transcript-activity-find",root:host,primary:true});
  return <div ref={setHost}><CommandActivityCard part={value} layout="chat" sessionId="activity-card-test" projectId="project" client={client}/></div>;
 });
 openFind();setFindQuery("UNIQUE_FOLDED_NEEDLE");
 await waitFor(()=>expect(findController.matches().length).toBeGreaterThan(0));
 findNext();
 await waitFor(()=>expect(view.container.textContent).toContain("5–5 of 5"));
 await waitFor(()=>expect(sink).toHaveBeenCalled());
 const opened=sink.mock.calls.at(-1)?.[0];
 expect(opened.document.toolCallId).toBe("source-4");
 expect(opened.document.pane).toContain("input");
});

it("retains each program's receipt scope when read activities combine calls", async () => {
 const { registerOpenFilesSurfaceSink,resetOpenFilesSurfaceForTests }=await import("../../platform/navigation/open-files-surface.ts");
 const { onTestFinished }=await import("vitest");
 onTestFinished(resetOpenFilesSurfaceForTests);
 const sink=vi.fn();registerOpenFilesSurfaceSink(sink);
 const value=part();const original=value.commandActivity!.calls[0]!;
 value.commandActivity!.calls.push({...original,id:"other-call",toolCallId:"other-call",commandProgram:{...program,id:"other-program"}});
 const get=vi.fn(async (_session:string,programId:string)=>({program:{...program,id:programId},executions:[{...receipt(),id:`${programId}:execution`,program_id:programId}],nodes:[],executions_omitted:0,nodes_omitted:0}));
 value.commandActivity!.members=[{...fact,execution_ids:["program:execution","other-program:execution"]}];
 const view=render(()=><CommandActivityCard part={value} layout="chat" sessionId="activity-card-test" projectId="project" client={stubClient({getCommandProgramReceipt:get})}/>);
 fireEvent.click(disclosureToggle(view.container.querySelector('[data-testid="tool-part-card"]')));
 await waitFor(()=>expect(view.container.textContent).toContain("Execution receipts and diagnostics"));
 fireEvent.click(view.getByRole("button",{name:/Execution receipts/}));
 const content=sink.mock.calls.at(-1)?.[0].document.content;
 expect(content.programs.map((program:{id:string})=>program.id)).toEqual(["program","other-program"]);
});

it("bounds the detail summary and opens every structural step in Files", async () => {
 const { registerOpenFilesSurfaceSink, resetOpenFilesSurfaceForTests } = await import("../../platform/navigation/open-files-surface.ts");
 const { onTestFinished } = await import("vitest");
 onTestFinished(resetOpenFilesSurfaceForTests);
 const sink = vi.fn(); registerOpenFilesSurfaceSink(sink);
 const value = part();
 value.commandActivity!.fact = { ...fact, steps: Array.from({ length: 1000 }, (_, index) => ({ id: String(index), command: `operation ${index} ${"long-input".repeat(100)}`, count: 10705, running: 0, waiting: 0, failed: 0, refused: 0, interrupted: 0, state: "completed", outcome: "Finished" })) };
 const get = vi.fn().mockResolvedValue({ program, executions: [receipt()], nodes: [], executions_omitted: 0, nodes_omitted: 0 });
 const view = render(() => <CommandActivityCard part={value} layout="chat" sessionId="activity-card-test" projectId="project" client={stubClient({ getCommandProgramReceipt: get })} />);
 fireEvent.click(disclosureToggle(view.container.querySelector('[data-testid="tool-part-card"]')));
 expect(view.container.querySelectorAll(".den-command-activity-step")).toHaveLength(3);
 for (const code of view.container.querySelectorAll(".den-command-activity-step code")) expect(code.textContent!.length).toBeLessThanOrEqual(81);
 expect(view.container.querySelector("pre")).toBeNull();
 fireEvent.click(view.getByRole("button", { name: /All 1000 steps/ }));
 const opened = sink.mock.calls.at(-1)?.[0];
 expect(opened.kind).toBe("chat-content");
 expect(opened.document.content.text).toContain(`operation 999 ${"long-input".repeat(100)}`);
 expect(opened.document.content.text.split("\n")).toHaveLength(1000);
});
