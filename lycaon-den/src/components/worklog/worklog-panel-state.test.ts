import { createRoot } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { createWorklogPanelState } from "./worklog-panel-state.ts";
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

describe("createWorklogPanelState", () => {
  it("opens and closes the panel", () => {
    createRoot((dispose) => {
      const worklog = createWorklogPanelState({
        appStore: { state: {} } as AppStore,
        sessionId: () => "sess-1",
        offline: () => false,
      });
      expect(worklog.isOpen()).toBe(false);
      worklog.open();
      expect(worklog.isOpen()).toBe(true);
      worklog.close();
      expect(worklog.isOpen()).toBe(false);
      dispose();
    });
  });

  it("refreshes progress and findings when opened", async () => {
    await new Promise<void>((resolve) => {
      createRoot((dispose) => {
        mocks.refreshProgress.mockClear();
        mocks.refreshFindings.mockClear();
        const worklog = createWorklogPanelState({
          appStore: { state: {} } as AppStore,
          sessionId: () => "sess-1",
          offline: () => false,
        });
        worklog.open();
        queueMicrotask(() => {
          expect(mocks.refreshProgress).toHaveBeenCalled();
          expect(mocks.refreshFindings).toHaveBeenCalled();
          dispose();
          resolve();
        });
      });
    });
  });
});
