import { expect, it, vi, beforeEach } from "vitest";
import { createSignal } from "solid-js";
import { render, waitFor } from "@solidjs/testing-library";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import type { CommandProgramReceipt } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { useCommandActivitySnapshots } from "./command-activity-snapshots.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { clearCommandActivities, observeCommandActivity } from "./command-activity-live.ts";
vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: vi.fn() }));

const program = { id: "retained-program", state: "completed", activity_revision: 10 } as CommandProgramReceipt;
const entry: Extract<ActivitySpanEntry, {kind:"tool"}> = { kind:"tool",part:{id:"row",toolCallId:"call",tool:"command",kind:"command",status:"completed",assistantMessageId:"assistant",messageId:"result",commandProgram:program} };
beforeEach(() => { vi.clearAllMocks(); clearCommandActivities("session"); });

it("resolves whole-program structural facts before presenting retained rows", async () => {
 const fact={id:"find",label:"Find paths",subject:"*.go",group:"Exploring project files",state:"completed",outcome:"Finished",execution_ids:["find"],spans:[],steps:[],execution_count:10705};
 const get=vi.fn().mockResolvedValue({program,activities:[fact],executions:[],nodes:[]});
 vi.mocked(getLycaonClient).mockReturnValue(stubClient({getCommandProgramReceipt:get}));
 const view=render(()=>{
  const snapshot=useCommandActivitySnapshots(()=>[entry],()=>"session");
  return <span>{snapshot(entry.part)?.activities?.[0]?.execution_count ?? "Loading"}</span>;
 });
 await waitFor(()=>expect(view.container.textContent).toBe("10705"));
 expect(get).toHaveBeenCalledOnce();
 expect(get).toHaveBeenCalledWith("session","retained-program",{limit:1});
});

it("preserves a command fallback when the host summary is unavailable", async () => {
 vi.mocked(getLycaonClient).mockReturnValue(stubClient({getCommandProgramReceipt:vi.fn().mockRejectedValue(new Error("offline"))}));
 const view=render(()=>{
  const snapshot=useCommandActivitySnapshots(()=>[entry],()=>"session");
  return <span>{snapshot(entry.part)?.activities === undefined ? "Recorded command" : "Loading"}</span>;
 });
 await waitFor(()=>expect(view.container.textContent).toBe("Recorded command"));
});

const STATES: CommandProgramReceipt["state"][] = ["running", "waiting", "completed", "failed", "rejected", "interrupted"];
function saved(state: CommandProgramReceipt["state"], steps: boolean): CommandProgramReceipt {
 const fact = { id: "server", label: "Run process", subject: "python3 -m http.server 8000", group: "Running programs", state, outcome: state, execution_ids: ["python"], spans: [], ...(steps ? { steps: [] } : {}) };
 return { id: `program-${state}-${steps}`, state, activity_revision: 15, activities: [fact] } as unknown as CommandProgramReceipt;
}
function row(program: CommandProgramReceipt, call: string): Extract<ActivitySpanEntry, { kind: "tool" }> {
 return { kind: "tool", part: { id: `row-${call}`, toolCallId: call, tool: "command", kind: "command", status: "running", assistantMessageId: "assistant", messageId: "result", commandProgram: program } };
}

// Every saved program, with or without recorded steps and a live event,
// resolves to the newest view: a live event as is, a settled program with
// steps as saved, and anything else from the host's receipt, keeping an
// unsettled program's recorded rows while that request is pending.
it("resolves every saved program to its newest view", async () => {
 for (const state of STATES) for (const steps of [false, true]) for (const live of [false, true]) {
  vi.clearAllMocks(); clearCommandActivities("session");
  const persisted = saved(state, steps), call = `call-${state}-${steps}-${live}`;
  const receipt = { ...saved("completed", true), id: persisted.id, activity_revision: 16 };
  const observed = { ...saved("failed", true), id: persisted.id, activity_revision: 17 };
  if (live) observeCommandActivity({ session_id: "session", tool_call_id: call, tool_name: "command", activity_id: "lease", kind: "running_tool", status: "done", started_at: "2026-11-13T05:09:44Z", progress: { phase: "executing", files_searched: 0, files_skipped: 0, matches: 0, command_program: observed } });
  let answer: (value: unknown) => void = () => {};
  const get = vi.fn().mockReturnValue(new Promise(resolve => { answer = resolve; }));
  vi.mocked(getLycaonClient).mockReturnValue(stubClient({ getCommandProgramReceipt: get }));
  const entry = row(persisted, call);
  let resolve!: ReturnType<typeof useCommandActivitySnapshots>;
  const view = render(() => { resolve = useCommandActivitySnapshots(() => [entry], () => "session"); return null; });
  const active = state === "running" || state === "waiting";
  const requested = !live && (active || !steps);
  if (live) expect(resolve(entry.part)).toEqual(observed);
  else if (!requested) expect(resolve(entry.part)).toBeUndefined();
  else expect(resolve(entry.part)).toEqual(active ? persisted : { ...persisted, activities: [] });
  answer({ program: receipt, activities: receipt.activities, executions: [], nodes: [] });
  if (requested) await waitFor(() => expect(resolve(entry.part)).toEqual(receipt));
  expect(get).toHaveBeenCalledTimes(requested ? 1 : 0);
  if (requested) expect(get).toHaveBeenCalledWith("session", persisted.id, { limit: 1 });
  view.unmount();
 }
});

it("does not re-fetch already-settled programs when pagination appends new entries", async () => {
 const progA = { id: "prog-a", state: "completed", activity_revision: 10 } as CommandProgramReceipt;
 const progB = { id: "prog-b", state: "completed", activity_revision: 11 } as CommandProgramReceipt;
 const entryA = row(progA, "call-a");
 const entryB = row(progB, "call-b");
 const factFor = (id: string) => ({ id, label: "Run", subject: id, group: "Running programs", state: "completed", outcome: "Finished", execution_ids: [id], spans: [], steps: [] });
 const get = vi.fn(async (_session: string, programId: string) => ({
  program: programId === "prog-a" ? progA : progB,
  activities: [factFor(programId)],
  executions: [],
  nodes: [],
 }));
 vi.mocked(getLycaonClient).mockReturnValue(stubClient({ getCommandProgramReceipt: get }));

 const [entries, setEntries] = createSignal<readonly ActivitySpanEntry[]>([entryA]);
 let resolve!: ReturnType<typeof useCommandActivitySnapshots>;
 const view = render(() => {
  resolve = useCommandActivitySnapshots(entries, () => "session");
  return <span>{resolve(entryA.part)?.activities?.[0]?.id ?? "loading"}:{resolve(entryB.part)?.activities?.[0]?.id ?? "none"}</span>;
 });
 await waitFor(() => expect(view.container.textContent).toBe("prog-a:none"));
 expect(get).toHaveBeenCalledTimes(1);

 setEntries([entryA, entryB]);
 await waitFor(() => expect(view.container.textContent).toBe("prog-a:prog-b"));
 expect(get).toHaveBeenCalledTimes(2);
 expect(get).toHaveBeenNthCalledWith(1, "session", "prog-a", { limit: 1 });
 expect(get).toHaveBeenNthCalledWith(2, "session", "prog-b", { limit: 1 });
 view.unmount();
});

it("reuses cached settled snapshots on remount while keeping running programs live and sessions isolated", async () => {
 const settled = { id: "prog-settled", state: "completed", activity_revision: 4 } as CommandProgramReceipt;
 const running = { id: "prog-running", state: "running", activity_revision: 5 } as CommandProgramReceipt;
 const settledEntry = row(settled, "call-settled");
 const runningEntry = row(running, "call-running");
 const fact = { id: "act", label: "Run", subject: "cmd", group: "Running programs", state: "completed", outcome: "Finished", execution_ids: ["x"], spans: [], steps: [] };
 const get = vi.fn(async (_session: string, programId: string) => ({
  program: programId === "prog-settled" ? settled : running,
  activities: [fact],
  executions: [],
  nodes: [],
 }));
 const client = stubClient({ getCommandProgramReceipt: get });
 vi.mocked(getLycaonClient).mockReturnValue(client);

 const first = render(() => {
  const snapshot = useCommandActivitySnapshots(() => [settledEntry], () => "session-1");
  return <span>{snapshot(settledEntry.part)?.activities?.[0]?.id ?? "loading"}</span>;
 });
 await waitFor(() => expect(first.container.textContent).toBe("act"));
 expect(get).toHaveBeenCalledTimes(1);
 first.unmount();

 // Remounting with the same client and session hits the settled cache with zero extra requests.
 const remounted = render(() => {
  const snapshot = useCommandActivitySnapshots(() => [settledEntry], () => "session-1");
  return <span>{snapshot(settledEntry.part)?.activities?.[0]?.id ?? "loading"}</span>;
 });
 expect(remounted.container.textContent).toBe("act");
 expect(get).toHaveBeenCalledTimes(1);
 remounted.unmount();

 // A different session does not share the cached snapshot.
 const otherSession = render(() => {
  const snapshot = useCommandActivitySnapshots(() => [settledEntry], () => "session-2");
  return <span>{snapshot(settledEntry.part)?.activities?.[0]?.id ?? "loading"}</span>;
 });
 await waitFor(() => expect(otherSession.container.textContent).toBe("act"));
 expect(get).toHaveBeenCalledTimes(2);
 otherSession.unmount();

 // Still-running programs are not cached, so remounting fetches fresh progress.
 const live1 = render(() => {
  const snapshot = useCommandActivitySnapshots(() => [runningEntry], () => "session-1");
  return <span>{snapshot(runningEntry.part)?.activities?.[0]?.id ?? "loading"}</span>;
 });
 await waitFor(() => expect(live1.container.textContent).toBe("act"));
 expect(get).toHaveBeenCalledTimes(3);
 live1.unmount();

 const live2 = render(() => {
  const snapshot = useCommandActivitySnapshots(() => [runningEntry], () => "session-1");
  return <span>{snapshot(runningEntry.part)?.activities?.[0]?.id ?? "loading"}</span>;
 });
 await waitFor(() => expect(get).toHaveBeenCalledTimes(4));
 live2.unmount();
});
