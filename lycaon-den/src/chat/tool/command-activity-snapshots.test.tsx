import { expect, it, vi, beforeEach } from "vitest";
import { render, waitFor } from "@solidjs/testing-library";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import type { CommandProgramReceipt } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { clearCommandActivitySnapshotCache, useCommandActivitySnapshots } from "./command-activity-snapshots.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { clearCommandActivities, observeCommandActivity } from "./command-activity-live.ts";
vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: vi.fn() }));

const program = { id: "retained-program", state: "completed", activity_revision: 10 } as CommandProgramReceipt;
const entry: Extract<ActivitySpanEntry, {kind:"tool"}> = { kind:"tool",part:{id:"row",toolCallId:"call",tool:"command",kind:"command",status:"completed",assistantMessageId:"assistant",messageId:"result",commandProgram:program} };
beforeEach(() => { vi.clearAllMocks(); clearCommandActivities("session"); clearCommandActivitySnapshotCache(); });

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

it("shares one read across concurrent consumers and reuses empty terminal summaries on remount", async () => {
 const get = vi.fn().mockResolvedValue({ program: { ...program, activity_revision: 11 }, activities: [], executions: [], nodes: [] });
 vi.mocked(getLycaonClient).mockReturnValue(stubClient({ getCommandProgramReceipt: get }));
 const mount = () => render(() => {
  const snapshot = useCommandActivitySnapshots(() => [entry], () => "session");
  return <span>{snapshot(entry.part)?.activity_revision}</span>;
 });
 const a = mount();
 const b = mount();
 await waitFor(() => expect([a.container.textContent, b.container.textContent]).toEqual(["11", "11"]));
 expect(get).toHaveBeenCalledTimes(1);
 a.unmount();
 b.unmount();

 const c = mount();
 expect(c.container.textContent).toBe("11");
 expect(get).toHaveBeenCalledTimes(1);
 c.unmount();
});

it("refreshes on revision or client change, retries after failure, and evicts beyond 32 entries", async () => {
 const revision = 20;
 const firstClient = stubClient({
  getCommandProgramReceipt: vi.fn(async (_sid, id) => {
   if (id === "fail-program") throw new Error("transient");
   return { program: { id, state: "completed", activity_revision: revision }, activities: [], executions: [], nodes: [] };
  }),
 });
 vi.mocked(getLycaonClient).mockReturnValue(firstClient);

 // Failed read is not cached and retries cleanly.
 const failEntry = row({ id: "fail-program", state: "completed", activity_revision: 1 } as CommandProgramReceipt, "fail-call");
 const failView = render(() => {
  const s = useCommandActivitySnapshots(() => [failEntry], () => "session");
  return <span>{s(failEntry.part)?.activities === undefined ? "err" : "ok"}</span>;
 });
 await waitFor(() => expect(failView.container.textContent).toBe("err"));
 failView.unmount();

 // Traverse 39 distinct keys under the 32-entry budget; oldest is evicted and re-fetched.
 for (let i = 0; i < 39; i++) {
  const e = row({ id: `prog-${i}`, state: "completed", activity_revision: 1 } as CommandProgramReceipt, `call-${i}`);
  const v = render(() => {
   const s = useCommandActivitySnapshots(() => [e], () => "session");
   return <span>{s(e.part)?.id ?? ""}</span>;
  });
  await waitFor(() => expect(v.container.textContent).toBe(`prog-${i}`));
  v.unmount();
 }
 const callsBefore = vi.mocked(firstClient.getCommandProgramReceipt).mock.calls.length;
 const evictedEntry = row({ id: "prog-0", state: "completed", activity_revision: 1 } as CommandProgramReceipt, "call-0");
 const evictedView = render(() => {
  const s = useCommandActivitySnapshots(() => [evictedEntry], () => "session");
  return <span>{s(evictedEntry.part)?.id ?? ""}</span>;
 });
 await waitFor(() => expect(evictedView.container.textContent).toBe("prog-0"));
 expect(vi.mocked(firstClient.getCommandProgramReceipt).mock.calls.length).toBe(callsBefore + 1);
 evictedView.unmount();

 // Client change does not reuse the previous client's cache.
 const secondClient = stubClient({
  getCommandProgramReceipt: vi.fn(async (_sid, id) => ({
   program: { id, state: "completed", activity_revision: 99 },
   activities: [], executions: [], nodes: [],
  })),
 });
 vi.mocked(getLycaonClient).mockReturnValue(secondClient);
 const warmOnFirst = row({ id: "prog-38", state: "completed", activity_revision: 1 } as CommandProgramReceipt, "call-38");
 const secondView = render(() => {
  const s = useCommandActivitySnapshots(() => [warmOnFirst], () => "session");
  return <span>{s(warmOnFirst.part)?.activity_revision ?? ""}</span>;
 });
 await waitFor(() => expect(secondView.container.textContent).toBe("99"));
 expect(secondClient.getCommandProgramReceipt).toHaveBeenCalledTimes(1);
 secondView.unmount();
});

it("refuses to cache a stale receipt revision and retries on remount to recover", async () => {
 let attempt = 0;
 const get = vi.fn(async () => {
  attempt += 1;
  // First response lags at revision 9 despite program requesting revision 10.
  const rev = attempt === 1 ? 9 : 10;
  return {
   program: { ...program, activity_revision: rev },
   activities: rev === 10 ? [{ id: "done", label: "Run", subject: "ok", group: "Run", state: "completed", outcome: "ok", execution_ids: [], spans: [], steps: [] }] : [],
   executions: [],
   nodes: [],
  };
 });
 vi.mocked(getLycaonClient).mockReturnValue(stubClient({ getCommandProgramReceipt: get }));

 const first = render(() => {
  const s = useCommandActivitySnapshots(() => [entry], () => "session");
  return <span>{s(entry.part)?.activity_revision ?? ""}</span>;
 });
 await waitFor(() => expect(get).toHaveBeenCalledTimes(1));
 await new Promise((resolve) => setTimeout(resolve, 0));
 // Stale revision 9 never downgrades the canonical revision 10.
 expect(first.container.textContent).toBe("10");
 first.unmount();

 // Remount must re-fetch rather than serving stale revision 9 from cache.
 const second = render(() => {
  const s = useCommandActivitySnapshots(() => [entry], () => "session");
  return <span>{`${s(entry.part)?.activity_revision}:${s(entry.part)?.activities?.length ?? 0}`}</span>;
 });
 await waitFor(() => expect(second.container.textContent).toBe("10:1"));
 expect(get).toHaveBeenCalledTimes(2);
 second.unmount();
});
