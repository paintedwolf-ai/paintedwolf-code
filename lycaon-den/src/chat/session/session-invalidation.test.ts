import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import { type AppStore } from "../../store/app-state-model.ts";
import {
  createSessionInvalidationSchedulers,
  SESSION_PROJECTION_COALESCE_MS,
} from "./session-invalidation.ts";

describe("session invalidation projections", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("drops a queued cost refresh after switching away and back", async () => {
    const store = createAppStore();
    const session = { id: "a", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "project", status: "idle" as const, posture: "build" as const, created_at: "t", activity_at: "t", updated_at: "t" };
    store.actions.setCurrentSession(session);
    const schedulers = createSessionInvalidationSchedulers(store, () => null, () => [], () => false);
    const first = vi.fn(async () => undefined);
    const stale = vi.fn(async () => undefined);
    schedulers.scheduleCost(first);
    await vi.advanceTimersByTimeAsync(0);
    schedulers.scheduleCost(stale);
    store.actions.setCurrentSession({ ...session, id: "b" });
    store.actions.setCurrentSession(session);
    await vi.advanceTimersByTimeAsync(5000);
    expect(first).toHaveBeenCalledOnce();
    expect(stale).not.toHaveBeenCalled();
    schedulers.cancel();
  });

  it("coalesces workflow and progress bursts independently", async () => {
    const schedulers = createSessionInvalidationSchedulers(
      {} as AppStore,
      () => null,
      () => [],
      () => false,
    );
    const workflow = vi.fn(async () => undefined);
    const progress = vi.fn(async () => undefined);

    schedulers.scheduleWorkflow(workflow);
    schedulers.scheduleWorkflow(workflow);
    schedulers.scheduleWorkflow(workflow);
    schedulers.scheduleProgress(progress);
    schedulers.scheduleProgress(progress);
    await vi.advanceTimersByTimeAsync(SESSION_PROJECTION_COALESCE_MS);

    expect(workflow).toHaveBeenCalledTimes(1);
    expect(progress).toHaveBeenCalledTimes(1);
    schedulers.cancel();
  });
});
