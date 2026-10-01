import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const invoke = vi.hoisted(() => vi.fn());

vi.mock("@tauri-apps/api/core", () => ({ invoke }));
vi.mock("../runtime.ts", () => ({ isTauriRuntime: () => true }));
vi.mock("./window-channel.ts", () => ({ listenHostEvent: vi.fn() }));
vi.mock("./window-subject.ts", () => ({ windowViewId: () => "main" }));

import {
  resetWorkspaceViewRegistryForTests,
  setCurrentWorkspaceContexts,
  type WorkspaceContext,
} from "./workspace-view-registry.ts";

const context = (sessionId: string): WorkspaceContext => ({
  kind: "session",
  projectId: "project-1",
  sessionId,
});

describe("workspace view publication", () => {
  let frames: FrameRequestCallback[];

  beforeEach(() => {
    frames = [];
    invoke.mockReset();
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      frames.push(callback);
      return frames.length;
    });
    vi.stubGlobal("cancelAnimationFrame", vi.fn());
    vi.spyOn(console, "debug").mockImplementation(() => undefined);
    resetWorkspaceViewRegistryForTests();
  });

  afterEach(() => {
    resetWorkspaceViewRegistryForTests();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("serializes host writes and collapses queued frames to the latest context", async () => {
    let releaseFirst!: () => void;
    invoke
      .mockImplementationOnce(
        () =>
          new Promise<void>((resolve) => {
            releaseFirst = resolve;
          }),
      )
      .mockResolvedValue(undefined);

    setCurrentWorkspaceContexts([context("session-1")]);
    frames.shift()?.(0);
    expect(invoke).toHaveBeenCalledTimes(1);

    setCurrentWorkspaceContexts([context("session-2")]);
    frames.shift()?.(1);
    setCurrentWorkspaceContexts([context("session-3")]);
    frames.shift()?.(2);
    expect(invoke).toHaveBeenCalledTimes(1);

    releaseFirst();
    await vi.waitFor(() => expect(invoke).toHaveBeenCalledTimes(2));
    await Promise.resolve();
    expect(invoke.mock.calls[1]?.[1]).toEqual({
      viewId: "main",
      contexts: [context("session-3")],
    });
  });

  it("allows an unchanged context to retry after publication fails", async () => {
    invoke
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue(undefined);
    const current = [context("session-1")];

    setCurrentWorkspaceContexts(current);
    frames.shift()?.(0);
    await vi.waitFor(() => expect(console.debug).toHaveBeenCalledTimes(1));

    setCurrentWorkspaceContexts(current);
    frames.shift()?.(1);
    await vi.waitFor(() => expect(invoke).toHaveBeenCalledTimes(2));
    await Promise.resolve();
  });
});
