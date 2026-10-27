// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRoot } from "solid-js";
import type { FileTabTransferSettled, FileTabWindowDrag } from "../../platform/windows/item-windows.ts";
import { createShellPeerWindows } from "../../components/shell/shell-peer-windows.ts";
import { createFilesTabTransfers } from "./files-tab-transfer.ts";
import { createFilesTabDrag } from "./files-tab-drag.ts";
import { applyFilesBufferDraft, applyFilesBufferLoad, filesBufferText, openFilesBuffer, resetProjectFilesForTests, type FilesBufferRelease } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { scheduleFilesDraftSync } from "../documents/files-draft-sync.ts";
import { required } from "../../test/at.ts";

const shell = vi.hoisted(() => {
  let listener: ((event: FileTabTransferSettled) => void) | undefined;
  return {
    beginTransfer: vi.fn(async () => "transfer-token"),
    ready: vi.fn(async (_token: string) => {}),
    abandon: vi.fn(async (_token: string) => {}),
    listen: vi.fn(async (handler: (event: FileTabTransferSettled) => void) => {
      listener = handler;
      return () => { listener = undefined; };
    }),
    settle(event: FileTabTransferSettled) {
      if (!listener) throw new Error("No transfer observer");
      listener(event);
    },
  };
});
const effects = vi.hoisted(() => ({
  publish: vi.fn(async () => true),
  persist: vi.fn(),
}));
vi.mock("../../platform/windows/item-windows.ts", async (original) => ({
  ...await original<typeof import("../../platform/windows/item-windows.ts")>(),
  beginFileTabTransfer: shell.beginTransfer,
  readyFileTabTransfer: shell.ready,
  abandonFileTabTransfer: shell.abandon,
  listenFileTabTransfer: shell.listen,
}));
vi.mock("../documents/editor-document.ts", async (original) => ({
  ...await original<typeof import("../documents/editor-document.ts")>(),
  publishEditorDocumentDraft: effects.publish,
}));
vi.mock("../editor/editor-session-fidelity.ts", async (original) => ({
  ...await original<typeof import("../editor/editor-session-fidelity.ts")>(),
  scheduleSessionFidelityPersist: effects.persist,
}));
vi.mock("../../chat/composer/chat-attachment-drag.ts", () => ({
  cancelPointerChatAttachmentDrag: vi.fn(),
  finishPointerChatAttachmentDrag: () => false,
  updatePointerChatAttachmentDrag: () => false,
}));

function deferred<T>() {
  let resolve: ((value: T) => void) | undefined;
  const promise = new Promise<T>((accept) => { resolve = accept; });
  return { promise, resolve(value: T) { required(resolve)(value); } };
}

const PROJECT = "handoff-project";
function fixture() {
  const key = openFilesBuffer(PROJECT, { rootId: "root", rootLabel: "Root", path: "draft.txt", intent: "permanent" });
  applyFilesBufferLoad(PROJECT, key, {
    file_id: "", version_id: "", workspace_id: "workspace", workspace_kind: "project",
    path: "draft.txt", content: "base", over_limit: false, writable: true,
    binary: false, size_bytes: 4, sha256: "base-sha",
  });
  scheduleFilesDraftSync(PROJECT, key, () => applyFilesBufferDraft(PROJECT, key, "latest draft"));
  const close = vi.fn(async (): Promise<FilesBufferRelease> => "released");
  const moved = vi.fn();
  const transfers = createFilesTabTransfers({ projectId: () => PROJECT, closeBuffer: close, onFileTabMovedOut: () => moved });
  return { key, close, moved, transfers };
}

beforeEach(() => {
  vi.clearAllMocks();
  resetProjectFilesForTests();
  effects.publish.mockResolvedValue(true);
});
afterEach(() => {
  window.dispatchEvent(new PointerEvent("pointercancel"));
  document.body.replaceChildren();
  resetProjectFilesForTests();
});

describe("compositor tab handoff", () => {
  it("publishes the debounced draft before ready and releases only after the shell commits", async () => {
    const f = fixture();
    const publication = deferred<boolean>();
    effects.publish.mockImplementationOnce(async () => {
      expect(filesBufferText(required(projectFilesState(PROJECT).byKey[f.key]))).toBe("latest draft");
      return publication.promise;
    });
    const stop = await f.transfers.observe();
    f.transfers.start(async () => "token", f.key);
    await vi.waitFor(() => expect(effects.publish).toHaveBeenCalledOnce());
    expect(shell.ready).not.toHaveBeenCalled();
    expect(f.close).not.toHaveBeenCalled();
    publication.resolve(true);
    await vi.waitFor(() => expect(shell.ready).toHaveBeenCalledWith("token"));
    expect(f.close).not.toHaveBeenCalled();
    shell.settle({ token: "token", outcome: "committed" });
    await vi.waitFor(() => expect(f.moved).toHaveBeenCalledWith(f.key));
    expect(f.close).toHaveBeenCalledWith(f.key);
    expect(effects.persist).toHaveBeenCalledOnce();
    stop();
  });

  it("abandons rejected publication without releasing the source", async () => {
    const f = fixture();
    effects.publish.mockResolvedValueOnce(false);
    f.transfers.start(async () => "token", f.key);
    await vi.waitFor(() => expect(shell.abandon).toHaveBeenCalledWith("token"));
    expect(shell.ready).not.toHaveBeenCalled();
    expect(f.close).not.toHaveBeenCalled();
  });

  it("retains a canceled source and a source restored by close recovery", async () => {
    const f = fixture();
    const stop = await f.transfers.observe();
    f.transfers.start(async () => "canceled", f.key);
    await vi.waitFor(() => expect(shell.ready).toHaveBeenCalledWith("canceled"));
    shell.settle({ token: "canceled", outcome: "cancelled" });
    expect(f.close).not.toHaveBeenCalled();
    f.close.mockResolvedValueOnce("restored");
    await f.transfers.releaseCommittedSource(f.key);
    expect(effects.persist).not.toHaveBeenCalled();
    expect(f.moved).not.toHaveBeenCalled();
    stop();
  });
});

function coordinateDrag(f: ReturnType<typeof fixture>, controller: FileTabWindowDrag) {
  const strip = document.createElement("div");
  strip.getBoundingClientRect = () => new DOMRect(0, 0, 200, 30);
  const tab = document.createElement("div");
  Object.assign(tab, { setPointerCapture() {}, hasPointerCapture: () => false, releasePointerCapture() {} });
  strip.append(tab);
  document.body.append(strip);
  const drag = createFilesTabDrag({
    projectId: () => PROJECT, state: () => projectFilesState(PROJECT), filesRoots: () => [],
    tabsElement: () => strip, scrollerElement: () => strip, closeBuffer: f.close,
    fileTabWindowDrag: () => controller, openInNewWindow: () => undefined, onFileTabMovedOut: () => f.moved,
  });
  tab.addEventListener("pointerdown", (event) => drag.onTabPointerDown(f.key, event));
  tab.dispatchEvent(new PointerEvent("pointerdown", { button: 0, pointerId: 1, clientX: 10, clientY: 10 }));
  window.dispatchEvent(new PointerEvent("pointermove", { pointerId: 1, clientX: 10, clientY: 100, screenX: 20, screenY: 200 }));
  return () => window.dispatchEvent(new PointerEvent("pointerup", { pointerId: 1, clientX: 10, clientY: 100, screenX: 20, screenY: 200 }));
}

describe("coordinate tab handoff", () => {
  it("waits for publication and outstanding movement before committing and releasing", async () => {
    const f = fixture();
    const publication = deferred<boolean>();
    const movement = deferred<boolean>();
    const controller = { begin: vi.fn(async () => "window"), move: vi.fn(() => movement.promise), finish: vi.fn(async () => true), cancel: vi.fn(async () => true) };
    effects.publish.mockImplementationOnce(async () => {
      expect(filesBufferText(required(projectFilesState(PROJECT).byKey[f.key]))).toBe("latest draft");
      return publication.promise;
    });
    const finish = coordinateDrag(f, controller);
    await vi.waitFor(() => expect(controller.move).toHaveBeenCalledOnce());
    finish();
    await vi.waitFor(() => expect(effects.publish).toHaveBeenCalledOnce());
    expect(controller.finish).not.toHaveBeenCalled();
    publication.resolve(true);
    await Promise.resolve();
    expect(controller.finish).not.toHaveBeenCalled();
    movement.resolve(true);
    await vi.waitFor(() => expect(f.moved).toHaveBeenCalledWith(f.key));
    expect(controller.finish).toHaveBeenCalledWith("window", { x: 20, y: 200 });
    expect(controller.cancel).not.toHaveBeenCalled();
    expect(effects.persist).toHaveBeenCalledOnce();
  });

  it.each(["publication", "commit"])("cancels failed %s without releasing the source", async (failure) => {
    const f = fixture();
    effects.publish.mockResolvedValueOnce(failure !== "publication");
    const controller = { begin: vi.fn(async () => "window"), move: vi.fn(async () => true), finish: vi.fn(async () => failure !== "commit"), cancel: vi.fn(async () => true) };
    const finish = coordinateDrag(f, controller);
    finish();
    await vi.waitFor(() => expect(controller.cancel).toHaveBeenCalledWith("window"));
    expect(f.close).not.toHaveBeenCalled();
    expect(effects.persist).not.toHaveBeenCalled();
    if (failure === "publication") expect(controller.finish).not.toHaveBeenCalled();
  });

  it("transfers an open file tab through shell peer windows and rejects missing context", async () => {
    const f = fixture();
    let currentProjectId: string | null = null;
    const peerWindows = createRoot((dispose) => {
      const created = createShellPeerWindows({
        shell: { state: { activeProjectId: null }, beginStageSwitch: vi.fn() } as never,
        appStore: { state: { currentSession: null, sidecarStatus: "disconnected" } } as never,
        projects: { byId: () => undefined } as never,
        activeProjectId: () => currentProjectId,
        activeProject: () => undefined,
        stageAvailable: () => false,
        itemSubject: null,
        stageColumnStageId: () => null,
        placeableStageId: () => null,
        splitLive: () => false,
        projectSessions: { state: { rows: [] } } as never,
        activeChat: () => null,
        openStage: vi.fn(),
      });
      dispose();
      return created;
    });
    await expect(peerWindows.transferFileTab(f.key)).rejects.toThrow("No project is bound to the file tab.");
    currentProjectId = PROJECT;
    await expect(peerWindows.transferFileTab("missing-key")).rejects.toThrow("The file tab is no longer open.");
    await expect(peerWindows.transferFileTab(f.key)).resolves.toBe("transfer-token");
    expect(shell.beginTransfer).toHaveBeenCalledWith({
      projectId: PROJECT,
      rootId: "root",
      path: "draft.txt",
      title: "draft.txt",
    });
  });
});
