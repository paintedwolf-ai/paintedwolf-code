import { createRoot, createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { bindWorklogRefresh } from "./worklog-panel-state.ts";
import type { AppStore } from "../../store/app-state-model.ts";

const mocks = vi.hoisted(() => ({
  refreshProgress: vi.fn(async () => undefined),
  refreshFindings: vi.fn(async () => undefined),
}));

vi.mock("../../chat/progress/progress-actions.ts", () => ({
  refreshProgress: mocks.refreshProgress,
}));

vi.mock("../../chat/actions/findings-actions.ts", () => ({
  refreshFindings: mocks.refreshFindings,
}));

vi.mock("../../platform/connection/app-connection.ts", () => ({
  getLycaonClient: () => ({ id: "client" }),
}));

function bind(offline: boolean) {
  const [open, setOpen] = createSignal(false);
  const dispose = createRoot((d) => {
    bindWorklogRefresh({
      appStore: { state: {} } as AppStore,
      sessionId: () => "sess-1",
      offline: () => offline,
      open,
    });
    return d;
  });
  return { setOpen, dispose };
}

describe("bindWorklogRefresh", () => {
  it("reads progress and findings once the worklog opens", async () => {
    vi.clearAllMocks();
    const { setOpen, dispose } = bind(false);
    await Promise.resolve();
    expect(mocks.refreshProgress).not.toHaveBeenCalled();
    setOpen(true);
    await Promise.resolve();
    expect(mocks.refreshProgress).toHaveBeenCalledOnce();
    expect(mocks.refreshFindings).toHaveBeenCalledOnce();
    dispose();
  });

  it("reads nothing while offline", async () => {
    vi.clearAllMocks();
    const { setOpen, dispose } = bind(true);
    setOpen(true);
    await Promise.resolve();
    expect(mocks.refreshProgress).not.toHaveBeenCalled();
    dispose();
  });
});
