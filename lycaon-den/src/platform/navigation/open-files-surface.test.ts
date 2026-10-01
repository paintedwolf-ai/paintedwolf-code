import { afterEach, describe, expect, it, vi } from "vitest";
import {
  applyOpenFilesSurfaceRequest,
  openFilesSurface,
  registerOpenFilesSurfaceSink,
  resetOpenFilesSurfaceForTests,
} from "./open-files-surface.ts";
import {
  getFilesStagePaneMode,
  getSidebarScope,
  resetFilesStagePaneForTests,
  setSidebarScope,
} from "../../files/review/review-pane.ts";
import { resetProjectFilesForTests } from "../../files/documents/project-files-buffers.ts";
import { projectFilesState } from "../../files/documents/files-buffer-state.ts";

const roots = [{ id: "r1", label: "repo", path: "/repo", is_primary: true }];

describe("open-files-surface", () => {
  afterEach(() => {
    resetOpenFilesSurfaceForTests();
    resetFilesStagePaneForTests();
    resetProjectFilesForTests();
  });

  it("queues until a sink is registered", () => {
    const sink = vi.fn();
    openFilesSurface({ kind: "stage", projectId: "p1" });
    expect(sink).not.toHaveBeenCalled();
    registerOpenFilesSurfaceSink(sink);
    expect(sink).toHaveBeenCalledTimes(1);
  });

  it("opens Files without changing the comparison", () => {
    setSidebarScope("p1", { kind: "commit" });
    applyOpenFilesSurfaceRequest({ kind: "stage", projectId: "p1" }, roots);

    expect(getFilesStagePaneMode("p1")).toBe("files");
    expect(getSidebarScope("p1")).toEqual({ kind: "commit" });
  });

  it("reuses a tool tab and keeps different calls and panes separate", () => {
    const document = { kind: "tool" as const, sessionId: "session", messageId: "message", toolCallId: "call", pane: "output", title: "Read output",
      content: { kind: "inline" as const, text: "Recorded output" } };
    const open = (value = document) => applyOpenFilesSurfaceRequest({ kind: "chat-content", projectId: "p1", document: value }, roots);
    open();
    const first = projectFilesState("p1").activeKey!;
    expect(projectFilesState("p1").byKey[first]).toMatchObject({ kind: "chat", loading: false, writable: null, chatContent: document });
    open({ ...document, pane: "args" });
    open({ ...document, toolCallId: "another" });
    expect(projectFilesState("p1").order).toHaveLength(3);
    open();
    expect(projectFilesState("p1").activeKey).toBe(first);
    expect(projectFilesState("p1").order).toHaveLength(3);
  });

  it("keeps approval documents distinct and reuses a checkpoint pane", () => {
    const document = {kind:"approval" as const,sessionId:"session",checkpointId:"checkpoint",pane:"targets",title:"Approval targets",content:{kind:"inline" as const,text:"first target"}};
    const open = (value = document) => applyOpenFilesSurfaceRequest({kind:"chat-content",projectId:"p1",document:value}, roots);
    open();
    const key = projectFilesState("p1").activeKey!;
    open({...document,content:{kind:"inline",text:"updated target"}});
    expect(projectFilesState("p1").activeKey).toBe(key);
    expect(projectFilesState("p1").byKey[key]?.chatContent?.content).toEqual({kind:"inline",text:"updated target"});
    open({...document,checkpointId:"another"});
    open({...document,pane:"details"});
    expect(projectFilesState("p1").order).toHaveLength(3);
  });

  it("opens the Review pane and a read-only preview diff", () => {
    applyOpenFilesSurfaceRequest({
      kind: "file-change-preview",
      projectId: "p1",
      rootId: "",
      path: "/repo/src/a.ts",
      before: "old",
      after: "new",
    }, roots);
    expect(getFilesStagePaneMode("p1")).toBe("review");
    const state = projectFilesState("p1");
    const buf = state.byKey[state.activeKey!];
    expect(buf?.kind).toBe("diff");
    expect(buf?.rootId).toBe("r1");
    expect(buf?.rootLabel).toBe("repo");
    expect(buf?.path).toBe("src/a.ts");
    expect(buf?.diffPreview?.version).toMatchObject({ rootId: "r1",
path: "src/a.ts",
beforeAvailability: "available", source: { kind: "text", before: "old", after: "new" } });
  });

  it("keeps separate approval snapshots for the same file", () => {
    const request = {
      kind: "file-change-preview" as const, projectId: "p1", rootId: "r1",
      path: "AGENTS.md", before: "original",
    };
    applyOpenFilesSurfaceRequest({ ...request, previewId: "first", after: "first proposal" }, roots);
    const firstKey = projectFilesState("p1").activeKey!;
    applyOpenFilesSurfaceRequest({ ...request, previewId: "second", after: "second proposal" }, roots);
    const state = projectFilesState("p1");
    expect(state.activeKey).not.toBe(firstKey);
    expect(state.byKey[state.activeKey!]?.diffPreview?.version.source).toMatchObject({ kind: "text", after: "second proposal" });
    applyOpenFilesSurfaceRequest({ ...request, previewId: "first", after: "first proposal" }, roots);
    expect(projectFilesState("p1").activeKey).toBe(firstKey);
    expect(projectFilesState("p1").byKey[firstKey]?.diffPreview?.version.source).toMatchObject({ kind: "text", after: "first proposal" });
  });
});
