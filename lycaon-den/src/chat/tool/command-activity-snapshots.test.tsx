import { expect, it, vi, beforeEach } from "vitest";
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

const step={execution_id:"python",label:"Run process",subject:"python3 -m http.server 8000",state:"running"};
function recorded(state:CommandProgramReceipt["state"]){
 const fact={id:"server",label:"Run process",subject:"python3 -m http.server 8000",group:"Running programs",state,outcome:state,execution_ids:["python"],spans:[],steps:[{...step,state}]};
 return {id:"detached",state,activity_revision:15,activities:[fact]} as unknown as CommandProgramReceipt;
}
function row(program:CommandProgramReceipt): Extract<ActivitySpanEntry,{kind:"tool"}> {
 return {kind:"tool",part:{id:"detached-row",toolCallId:"detached-call",tool:"command",kind:"command",status:"running",assistantMessageId:"assistant",messageId:"result",commandProgram:program}};
}

it("reads the settled receipt for a program recorded while it was still running", async () => {
 const persisted=recorded("running"), settled={...recorded("failed"),activity_revision:16};
 let answer:(value:unknown)=>void=()=>{};
 const get=vi.fn().mockReturnValue(new Promise(resolve=>{answer=resolve;}));
 vi.mocked(getLycaonClient).mockReturnValue(stubClient({getCommandProgramReceipt:get}));
 const view=render(()=>{
  const snapshot=useCommandActivitySnapshots(()=>[row(persisted)],()=>"session");
  return <span>{snapshot(row(persisted).part)?.activities?.[0]?.state ?? "blank"}</span>;
 });
 expect(view.container.textContent).toBe(persisted.activities![0]!.state);
 answer({program:settled,activities:settled.activities,executions:[],nodes:[]});
 await waitFor(()=>expect(view.container.textContent).toBe(settled.activities![0]!.state));
 expect(get).toHaveBeenCalledOnce();
 expect(get).toHaveBeenCalledWith("session",persisted.id,{limit:1});
});

it("lets a live event stand in for the receipt", () => {
 const persisted=recorded("running"), live={...recorded("completed"),activity_revision:16};
 observeCommandActivity({session_id:"session",tool_call_id:"detached-call",tool_name:"command",activity_id:"lease",kind:"running_tool",status:"done",started_at:"2026-11-13T05:09:44Z",progress:{phase:"executing",files_searched:0,files_skipped:0,matches:0,command_program:live}});
 const get=vi.fn();
 vi.mocked(getLycaonClient).mockReturnValue(stubClient({getCommandProgramReceipt:get}));
 const view=render(()=>{
  const snapshot=useCommandActivitySnapshots(()=>[row(persisted)],()=>"session");
  return <span>{snapshot(row(persisted).part)?.state}</span>;
 });
 expect(view.container.textContent).toBe(live.state);
 expect(get).not.toHaveBeenCalled();
});

it("keeps a settled program with recorded steps without a request", () => {
 const persisted=recorded("completed");
 const get=vi.fn();
 vi.mocked(getLycaonClient).mockReturnValue(stubClient({getCommandProgramReceipt:get}));
 const view=render(()=>{
  const snapshot=useCommandActivitySnapshots(()=>[row(persisted)],()=>"session");
  return <span>{snapshot(row(persisted).part)?.state ?? "persisted"}</span>;
 });
 expect(view.container.textContent).toBe("persisted");
 expect(get).not.toHaveBeenCalled();
});
