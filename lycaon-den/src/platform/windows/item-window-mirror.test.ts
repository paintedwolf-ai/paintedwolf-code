import { afterEach, describe, expect, it, vi } from "vitest";
import {
  abandonFileTabTransfer,
  beginFileTabTransfer,
  commitFileTabTransfer,
  itemWindowViews,
  listenFileTabTransfer,
  readyFileTabTransfer,
  returnFileTabTransfer,
  startItemWindowViewMirror,
  type FileTabTransferSettled,
  type ItemWindowView,
} from "./item-windows.ts";

const host = vi.hoisted(() => ({
  invoke: vi.fn<(...args: unknown[]) => Promise<unknown>>(),
  listen: vi.fn<(event: string, handler: (event: { payload: ItemWindowView[] }) => void) => Promise<() => void>>(),
}));
vi.mock("@tauri-apps/api/core", () => ({ invoke: host.invoke }));
vi.mock("../runtime.ts", () => ({ isTauriRuntime: () => true }));
vi.mock("./window-channel.ts", () => ({ listenHostEvent: host.listen }));
let stop: (() => void) | undefined;
afterEach(() => { stop?.(); stop = undefined; vi.resetAllMocks(); });

describe("window registry mirror", () => {
  it("keeps a live registry update that arrives before the initial list response", async () => {
    const latest: ItemWindowView[] = [{ label: "file:7", title: "notes.txt", viewNumber: 7, kind: "file", projectId: "p" }];
    host.listen.mockImplementation(async (_event, handler) => { handler({ payload: latest }); return () => undefined; });
    host.invoke.mockResolvedValue([]);
    stop = await startItemWindowViewMirror();
    expect(itemWindowViews()).toEqual(latest);
  });

  it("shares one subscription until its last subscriber leaves and never replays a stale list", async () => {
    let deliver: (event: { payload: ItemWindowView[] }) => void = () => {};
    const unlisten = vi.fn();
    host.listen.mockImplementation(async (_event, handler) => { deliver = handler; return unlisten; });
    let resolveList: (value: ItemWindowView[]) => void = () => {};
    host.invoke.mockImplementation(() => new Promise(resolve => { resolveList = resolve; }));
    const first = startItemWindowViewMirror();
    const second = startItemWindowViewMirror();
    await vi.waitFor(() => expect(host.invoke).toHaveBeenCalledOnce());
    deliver({ payload: [] });
    resolveList([{ label: "file:2", title: "closed.txt", viewNumber: 2, kind: "file", projectId: "p" }]);
    const stopFirst = await first;
    stop = await second;
    expect(itemWindowViews()).toEqual([]);
    stopFirst(); stopFirst();
    expect(unlisten).not.toHaveBeenCalled();
    const latest: ItemWindowView[] = [{ label: "file:9", title: "new.txt", viewNumber: 9, kind: "file", projectId: "p" }];
    deliver({ payload: latest });
    expect(itemWindowViews()).toEqual(latest);
    expect(host.listen).toHaveBeenCalledOnce();
    expect(host.invoke).toHaveBeenCalledOnce();
    stop(); stop = undefined;
    expect(unlisten).toHaveBeenCalledOnce();
    deliver({ payload: [] });
    expect(itemWindowViews()).toEqual(latest);
  });

  it("can retry subscription setup after a native listener failure", async () => {
    host.listen.mockRejectedValueOnce(new Error("Host unavailable"));
    await expect(startItemWindowViewMirror()).rejects.toThrow("Host unavailable");
    const unlisten = vi.fn();
    host.listen.mockResolvedValue(unlisten);
    host.invoke.mockResolvedValue([]);
    stop = await startItemWindowViewMirror();
    expect(host.listen).toHaveBeenCalledTimes(2);
    stop(); stop = undefined;
    expect(unlisten).toHaveBeenCalledOnce();
  });

  it("forwards native file tab transfer lifecycle commands and settlement events", async () => {
    host.invoke.mockResolvedValueOnce("transfer-token");
    await expect(
      beginFileTabTransfer({ projectId: "p", rootId: "root", path: "a.ts", title: "a.ts" }),
    ).resolves.toBe("transfer-token");
    expect(host.invoke).toHaveBeenCalledWith("begin_native_file_tab_drag", {
      projectId: "p",
      rootId: "root",
      path: "a.ts",
      title: "a.ts",
    });

    await readyFileTabTransfer("transfer-token");
    expect(host.invoke).toHaveBeenCalledWith("ready_file_tab_transfer", { token: "transfer-token" });

    await abandonFileTabTransfer("transfer-token");
    expect(host.invoke).toHaveBeenCalledWith("abandon_file_tab_transfer", { token: "transfer-token" });

    await commitFileTabTransfer("transfer-token");
    expect(host.invoke).toHaveBeenCalledWith("commit_file_tab_transfer", { token: "transfer-token" });

    await returnFileTabTransfer("transfer-token");
    expect(host.invoke).toHaveBeenCalledWith("return_file_tab_transfer", { token: "transfer-token" });

    const settled: FileTabTransferSettled[] = [];
    const unlisten = vi.fn();
    host.listen.mockImplementationOnce(async (event, handler) => {
      expect(event).toBe("file-tab-transfer");
      (handler as unknown as (e: { payload: FileTabTransferSettled }) => void)({
        payload: { token: "transfer-token", outcome: "committed" },
      });
      return unlisten;
    });
    const stopTransfer = await listenFileTabTransfer((event) => settled.push(event));
    expect(settled).toEqual([{ token: "transfer-token", outcome: "committed" }]);
    stopTransfer();
  });
});
