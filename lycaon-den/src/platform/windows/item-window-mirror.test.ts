import { afterEach, describe, expect, it, vi } from "vitest";
import { itemWindowViews, startItemWindowViewMirror, type ItemWindowView } from "./item-windows.ts";

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
});
