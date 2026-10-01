import { describe, expect, it } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import { stubClient } from "../../test/client-fixture.ts";
import type { CoordinatorRunContext } from "../../api/types.ts";
import { refreshCoordinatorContext } from "./coordinator-context-actions.ts";

describe("coordinator context refresh ordering", () => {
  it("does not let a background follow-up supersede the foreground refresh", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({ id: "b", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
    let finish!: (context: CoordinatorRunContext) => void;
    const response = new Promise<CoordinatorRunContext>((resolve) => { finish = resolve; });
    const foreground = refreshCoordinatorContext(store, stubClient({ getCoordinatorContext: () => response }), "b");
    await refreshCoordinatorContext(store, stubClient({ getCoordinatorContext: async () => ({ current_phase: "background" }) }), "a");
    finish({ current_phase: "foreground" });
    await foreground;
    expect(store.state.coordinatorRunContext?.current_phase).toBe("foreground");
  });

  it("keeps the newer request when an earlier response arrives later", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({ id: "s1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
    let finish!: (context: CoordinatorRunContext) => void;
    const response = new Promise<CoordinatorRunContext>((resolve) => { finish = resolve; });
    const old = refreshCoordinatorContext(store, stubClient({ getCoordinatorContext: () => response }), "s1");
    await refreshCoordinatorContext(store, stubClient({ getCoordinatorContext: async () => ({ current_phase: "new" }) }), "s1");
    finish({ current_phase: "old" });
    await old;
    expect(store.state.coordinatorRunContext?.current_phase).toBe("new");
  });
});
