import { describe, expect, it, vi } from "vitest";
import type { ProjectBackgroundProcess, ProjectRemovalAssessment, ProjectRemovalRequest, ProjectRemovalResult } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { BackendTransportError } from "../../platform/connection/request-connectivity.ts";
import { createProjectRemoval } from "./project-removal.ts";
import { noticeFromUnknown } from "../../notices/notice-model.ts";
import { APP_SCOPE } from "../../notices/notice-scope.ts";

const assessment: ProjectRemovalAssessment = {project_id:"p", assessment_token:"review", extension_revision:"device", complete:true, checks:[], extensions:[{pack_id:"acme/one",disposition:"eligible",reasons:[]}]};
const result: ProjectRemovalResult = {operation_id:"op",project_id:"p",project_state:"deleted",cleanup_state:"removed",extensions:["acme/one"],reason:"",failure_code:"",documents:0,sessions:0,workers:0,overlays:0};
function setup() {
  let id = 0;
  const client = {assessProjectRemoval:vi.fn(async(_id: string)=>assessment),createProjectRemoval:vi.fn(async(_id: string, _request: ProjectRemovalRequest)=>result),getProjectRemoval:vi.fn(async(_id: string, _operationId: string)=>result),listProjectBackgroundProcesses:vi.fn(async(_id: string)=>({processes:[] as ProjectBackgroundProcess[],revision:1}))};
  const confirm = vi.fn(async(_request: import("../../platform/interaction/confirm-dialog.ts").ConfirmDestructiveRequest)=>true);
  const review = vi.fn(async (_assessment: ProjectRemovalAssessment | null): Promise<string[] | null> => ["acme/one"]);
  const prepare = vi.fn(async()=>{});
  const retire = vi.fn(async()=>{});
  const report = vi.fn();
  const notify = vi.fn();
  const extensionsChanged = vi.fn();
  const progress = vi.fn();
  const pause = vi.fn(async (_unanswered: number) => {});
  const remove = createProjectRemoval({client:()=>client,confirm,review,prepare,retire,report,notify,extensionsChanged,progress,pause,operationId:()=>`op-${++id}`});
  return {client,confirm,review,prepare,retire,report,notify,extensionsChanged,progress,pause,remove};
}
describe("project removal controller",()=>{
  it("serializes bulk reviews and shares completion for duplicate requests", async () => {
    const f = setup();
    let resolve!: (ids: string[] | null) => void;
    f.review.mockImplementationOnce(() => new Promise((done) => { resolve = done; }));
    const first = f.remove("p");
    expect(f.remove("p")).toBe(first);
    const next = f.remove("q");
    await vi.waitFor(() => expect(f.review).toHaveBeenCalledOnce());
    expect(f.client.assessProjectRemoval).toHaveBeenCalledTimes(1);
    resolve(null);
    await Promise.all([first, next]);
    expect(f.client.createProjectRemoval).toHaveBeenCalledTimes(1);
    expect(f.client.createProjectRemoval).toHaveBeenCalledWith("q", expect.anything());
  });
  it("submits only the host-assessed selection and retires after the host result",async()=>{
    const f=setup();await f.remove("p");
    expect(f.client.createProjectRemoval).toHaveBeenCalledWith("p",{operation_id:"op-1",assessment_token:"review",remove_extensions:["acme/one"],force:false});
    expect(f.retire).toHaveBeenCalledWith("p");expect(f.extensionsChanged).toHaveBeenCalledOnce();
  });
  it("keeps extensions when the person declines cleanup",async()=>{
    const f=setup();f.review.mockResolvedValue([]);await f.remove("p");
    expect(f.client.createProjectRemoval).toHaveBeenCalledWith("p",expect.objectContaining({remove_extensions:[]}));
  });
  it("confirms single deletion via review even when there are no extensions", async () => {
    const f = setup();
    f.client.assessProjectRemoval.mockResolvedValueOnce({ ...assessment, extensions: [] });
    f.review.mockResolvedValueOnce([]);
    await f.remove("p");
    expect(f.review).toHaveBeenCalledWith(expect.objectContaining({ extensions: [] }), "p");
    expect(f.client.createProjectRemoval).toHaveBeenCalledWith("p", expect.objectContaining({ remove_extensions: [] }));
  });
  it("skips redundant review for confirmed bulk deletion when there are no extensions", async () => {
    const f = setup();
    f.client.assessProjectRemoval.mockResolvedValueOnce({ ...assessment, extensions: [] });
    await f.remove("p", { confirmed: true });
    expect(f.review).not.toHaveBeenCalled();
    expect(f.client.createProjectRemoval).toHaveBeenCalledWith("p", expect.objectContaining({ remove_extensions: [] }));
  });
  it("still reviews confirmed bulk deletion when extensions are present", async () => {
    const f = setup();
    f.review.mockResolvedValueOnce(["acme/one"]);
    await f.remove("p", { confirmed: true });
    expect(f.review).toHaveBeenCalledWith(expect.objectContaining({ extensions: expect.any(Array) }), "p");
    expect(f.client.createProjectRemoval).toHaveBeenCalledWith("p", expect.objectContaining({ remove_extensions: ["acme/one"] }));
  });
  it("never turns unknown evidence into a removable selection",async()=>{
    const f=setup();f.client.assessProjectRemoval.mockResolvedValue({...assessment,complete:false,extensions:[{pack_id:"acme/one",disposition:"unknown",reasons:[{code:"incomplete_project_evidence",subject_id:""}]}],checks:[{project_id:"other",name:"Other",state:"unknown",revision:"",reason:"manifest_unreadable"}]});
    f.review.mockResolvedValue([]);await f.remove("p");expect(f.client.createProjectRemoval).toHaveBeenCalledWith("p",expect.objectContaining({remove_extensions:[]}));
    expect(f.review).toHaveBeenCalledWith(expect.objectContaining({complete:false}), "p");
  });
  it("does not retire or force a busy project after cancellation",async()=>{
    const f=setup();f.client.createProjectRemoval.mockResolvedValue({...result,project_state:"retained",cleanup_state:"retained",failure_code:"root_busy",documents:2});
    f.confirm.mockResolvedValueOnce(false);
    await f.remove("p");expect(f.client.createProjectRemoval).toHaveBeenCalledOnce();expect(f.retire).not.toHaveBeenCalled();
  });
  it("records a distinct explicitly forced request",async()=>{
    const f=setup();f.client.createProjectRemoval.mockResolvedValueOnce({...result,project_state:"retained",cleanup_state:"retained",failure_code:"root_busy",workers:1}).mockResolvedValueOnce(result);
    await f.remove("p");expect(f.client.createProjectRemoval).toHaveBeenLastCalledWith("p",expect.objectContaining({force:true,operation_id:"op-2"}));
  });
  it("reads a recorded result after the mutation response is lost",async()=>{
    const f=setup();f.client.createProjectRemoval.mockRejectedValueOnce(new Error("offline"));await f.remove("p");
    expect(f.client.getProjectRemoval).toHaveBeenCalledWith("p","op-1");expect(f.retire).toHaveBeenCalledOnce();
  });
  it("keeps the operation identity when both responses are lost",async()=>{
    const f=setup();f.client.createProjectRemoval.mockRejectedValueOnce(new Error("offline"));f.client.getProjectRemoval.mockRejectedValueOnce(new Error("offline"));await f.remove("p");await f.remove("p");
    expect(f.client.createProjectRemoval.mock.calls[0]).toEqual(f.client.createProjectRemoval.mock.calls[1]);expect(f.client.assessProjectRemoval).toHaveBeenCalledOnce();
    expect(f.report).toHaveBeenCalledWith(expect.objectContaining({ cause: expect.any(Error) }));
    expect(noticeFromUnknown(f.report.mock.calls[0]?.[0], APP_SCOPE)).toMatchObject({code: "project_removal_unconfirmed", message: expect.stringContaining("could not be confirmed")});
  });
  it("reports cleanup failure while applying successful project deletion",async()=>{
    const f=setup();f.client.createProjectRemoval.mockResolvedValue({...result,cleanup_state:"failed",reason:"Cleanup failed"});await f.remove("p");
    expect(f.retire).toHaveBeenCalledOnce();expect(f.notify).toHaveBeenCalledWith(expect.objectContaining({message:"Cleanup failed",severity:"warning"}));
  });
  it("requires a fresh review after the host rejects stale evidence",async()=>{
    const f=setup();f.client.createProjectRemoval.mockRejectedValueOnce(new LycaonApiError("stale",409,"project_removal_assessment_changed"));await f.remove("p");await f.remove("p");
    expect(f.client.assessProjectRemoval).toHaveBeenCalledTimes(2);expect(f.client.getProjectRemoval).not.toHaveBeenCalled();
  });
  it("lists running commands on busy project removal and refreshes review if the running set changes before admission", async () => {
    const f = setup();
    const bg1 = {
      process_id: "bg_1",
      project_id: "p",
      session_id: "s-1",
      session_title: "Chat 1",
      session_archived: false,
      command: "python3 -m http.server 8000",
      working_dir: ".",
      started_at: "2026-10-30T12:00:00Z",
      running: true,
      ports: ["127.0.0.1:8000"],
    };
    const bg2 = {
      ...bg1,
      process_id: "bg_2",
      command: "npm run dev",
      ports: ["127.0.0.1:5173"],
    };
    f.client.assessProjectRemoval.mockResolvedValueOnce({ ...assessment, background_processes: [bg1] });
    f.client.listProjectBackgroundProcesses
      .mockResolvedValueOnce({ processes: [bg1, bg2], revision: 1 })
      .mockResolvedValueOnce({ processes: [bg1, bg2], revision: 2 })
      .mockResolvedValueOnce({ processes: [bg1, bg2], revision: 2 });
    f.review.mockResolvedValue(["acme/one"]);
    f.client.createProjectRemoval
      .mockResolvedValueOnce({
        ...result,
        project_state: "retained",
        cleanup_state: "retained",
        failure_code: "root_busy",
        background_processes: [bg1, bg2],
      })
      .mockResolvedValueOnce(result);
    await f.remove("p");
    expect(f.review).toHaveBeenCalledTimes(2);
    expect(f.confirm).toHaveBeenCalledWith(
      expect.objectContaining({
        message: expect.stringContaining("python3 -m http.server 8000 (127.0.0.1:8000) — Chat 1"),
      }),
    );
    expect(f.client.createProjectRemoval).toHaveBeenLastCalledWith(
      "p",
      expect.objectContaining({ force: true }),
    );
  });
});

it("polls an accepted operation until settlement without retiring early", async () => {
  const f = setup();
  const running: ProjectRemovalResult = { ...result, project_state: "unknown", progress: { phase: "chats" as const, completed: 1, total: 3 } };
  f.client.createProjectRemoval.mockResolvedValueOnce(running);
  f.client.getProjectRemoval.mockResolvedValueOnce(running).mockResolvedValueOnce(result);
  f.pause.mockImplementation(async () => { expect(f.retire).not.toHaveBeenCalled(); });
  await f.remove("p");
  expect(f.client.createProjectRemoval).toHaveBeenCalledOnce();
  expect(f.client.getProjectRemoval).toHaveBeenCalledTimes(2);
  expect(f.client.getProjectRemoval).toHaveBeenLastCalledWith("p", "op-1");
  expect(f.progress.mock.calls).toEqual([["p", running.progress], ["p", null]]);
  expect(f.retire).toHaveBeenCalledOnce();
  expect(f.notify).not.toHaveBeenCalled();
});
it("keeps the accepted operation identity when status becomes unreachable", async () => {
  const f = setup();
  f.client.createProjectRemoval.mockResolvedValueOnce({ ...result, project_state: "unknown", progress: { phase: "preparing", completed: 0, total: 0 } });
  f.client.getProjectRemoval.mockRejectedValueOnce(new Error("offline"));
  await f.remove("p");
  expect(f.retire).not.toHaveBeenCalled();
  expect(f.progress).toHaveBeenLastCalledWith("p", null);
  await f.remove("p");
  expect(f.client.createProjectRemoval.mock.calls[0]).toEqual(f.client.createProjectRemoval.mock.calls[1]);
  expect(f.report).toHaveBeenCalledOnce();
});
it("keeps polling an accepted operation through a transport gap", async () => {
  const f = setup();
  const running: ProjectRemovalResult = { ...result, project_state: "unknown", progress: { phase: "files" as const, completed: 0, total: 0 } };
  f.client.createProjectRemoval.mockResolvedValueOnce(running);
  f.client.getProjectRemoval
    .mockRejectedValueOnce(new BackendTransportError(new TypeError("Failed to fetch"), "reachable"))
    .mockResolvedValueOnce(running)
    .mockResolvedValueOnce(result);
  await f.remove("p");
  expect(f.pause.mock.calls.map(([unanswered]) => unanswered)).toEqual([0, 1, 0]);
  expect(f.client.getProjectRemoval).toHaveBeenCalledTimes(3);
  expect(f.progress.mock.calls).toEqual([["p", running.progress], ["p", null]]);
  expect(f.retire).toHaveBeenCalledOnce();
  expect(f.report).not.toHaveBeenCalled();
});
it("reports an unconfirmed outcome once status stays unanswered", async () => {
  const f = setup();
  f.client.createProjectRemoval.mockResolvedValueOnce({ ...result, project_state: "unknown", progress: { phase: "preparing" as const, completed: 0, total: 0 } });
  f.client.getProjectRemoval.mockRejectedValue(new BackendTransportError(new TypeError("Failed to fetch"), "reachable"));
  await f.remove("p");
  expect(f.client.getProjectRemoval).toHaveBeenCalledTimes(8);
  expect(f.retire).not.toHaveBeenCalled();
  expect(f.report).toHaveBeenCalledOnce();
  expect(f.progress).toHaveBeenLastCalledWith("p", null);
});

it("recovers through transport gaps before the first status response", async () => {
  const f = setup();
  const offline = new BackendTransportError(new TypeError("Failed to fetch"), "reachable");
  f.client.createProjectRemoval.mockRejectedValueOnce(offline);
  f.client.getProjectRemoval.mockRejectedValueOnce(offline).mockRejectedValueOnce(offline).mockResolvedValueOnce(result);
  await f.remove("p");
  expect(f.client.createProjectRemoval).toHaveBeenCalledOnce();
  expect(f.client.getProjectRemoval.mock.calls).toEqual([["p", "op-1"], ["p", "op-1"], ["p", "op-1"]]);
  expect(f.pause.mock.calls).toEqual([[1], [2]]);
  expect(f.retire).toHaveBeenCalledOnce();
  expect(f.report).not.toHaveBeenCalled();
});
it("bounds initial status recovery and preserves the exact request for retry", async () => {
  const f = setup();
  const offline = new BackendTransportError(new TypeError("Failed to fetch"), "reachable");
  f.client.createProjectRemoval.mockRejectedValueOnce(offline);
  f.client.getProjectRemoval.mockRejectedValue(offline);
  await f.remove("p");
  expect(f.client.getProjectRemoval).toHaveBeenCalledTimes(8);
  expect(f.report).toHaveBeenCalledOnce();
  expect(f.retire).not.toHaveBeenCalled();
  await f.remove("p");
  expect(f.client.createProjectRemoval.mock.calls[0]).toEqual(f.client.createProjectRemoval.mock.calls[1]);
  expect(f.retire).toHaveBeenCalledOnce();
});
