import { expect, it, vi, beforeEach } from "vitest";
import { render, waitFor } from "@solidjs/testing-library";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import type { CommandProgramReceipt } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { useCommandActivitySnapshots } from "./command-activity-snapshots.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: vi.fn() }));

const program = { id: "retained-program", state: "completed", activity_revision: 10 } as CommandProgramReceipt;
const entry: Extract<ActivitySpanEntry, {kind:"tool"}> = { kind:"tool",part:{id:"row",toolCallId:"call",tool:"command",kind:"command",status:"completed",assistantMessageId:"assistant",messageId:"result",commandProgram:program} };
beforeEach(() => vi.clearAllMocks());

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
