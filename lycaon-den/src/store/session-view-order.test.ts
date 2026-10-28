import { describe, expect, it } from "vitest";
import { createAppStore } from "./app-state.ts";
import { valueOf } from "./load-state.ts";

function fixture() {
  const store = createAppStore();
  store.actions.setCurrentSession({ id: "s1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
  return store;
}

describe("session view snapshot ordering", () => {
  it("keeps newer progress and findings revisions", () => {
    const store = fixture();
    const epoch = store.state.sessionViewEpoch;
    store.actions.setProgress("s1", epoch, { revision: 2, steps: [{ state: "done", label: "Current" }] });
    store.actions.setFindings("s1", epoch, { revision: 2, findings: [] });
    store.actions.setProgress("s1", epoch, { revision: 1, steps: [] });
    store.actions.setFindings("s1", epoch, { revision: 1, findings: [] });
    expect(store.state.progress?.revision).toBe(2);
    expect(store.state.progress?.steps[0]?.label).toBe("Current");
    expect(valueOf(store.state.findings)?.revision).toBe(2);
  });

  it("accepts lower revisions from a replacement backend while retaining data until they arrive", () => {
    const store = fixture();
    store.actions.setProgress("s1", store.state.sessionViewEpoch, { revision: 100, steps: [] });
    store.actions.setFindings("s1", store.state.sessionViewEpoch, { revision: 100, findings: [] });
    store.actions.setQueueDraft("s1", store.state.sessionViewEpoch, { revision: 100, queue_items: [], hold: false, sending: false });
    store.actions.invalidateSessionViewRequests();
    expect(store.state.progress?.revision).toBe(100);
    expect(valueOf(store.state.findings)?.revision).toBe(100);
    store.actions.setProgress("s1", store.state.sessionViewEpoch, { revision: 1, steps: [] });
    store.actions.setFindings("s1", store.state.sessionViewEpoch, { revision: 1, findings: [] });
    store.actions.setQueueDraft("s1", store.state.sessionViewEpoch, { revision: 1, queue_items: [], hold: false, sending: false });
    expect(store.state.progress?.revision).toBe(1);
    expect(valueOf(store.state.findings)?.revision).toBe(1);
    expect(store.state.queueDraft?.revision).toBe(1);
  });

  it("invalidates in-flight view writes without clearing the visible session", () => {
    const store = fixture();
    const epoch = store.state.sessionViewEpoch;
    store.actions.setProgress("s1", epoch, { revision: 2, steps: [] });
    store.actions.invalidateSessionViewRequests();
    store.actions.setProgress("s1", epoch, { revision: 3, steps: [] });
    expect(store.state.currentSession?.id).toBe("s1");
    expect(store.state.progress?.revision).toBe(2);
  });

  it("retains queueDrafts across session switch and invalidateSessionViewRequests", () => {
    const store = fixture();
    const queue = { revision: 1, queue_items: [{ id: "q1", text: "first", submitted_by: "person", created_at: "t" }], hold: false, sending: false };
    store.actions.setQueueDraft("s1", store.state.sessionViewEpoch, queue);
    expect(store.state.queueDraft?.queue_items[0]?.text).toBe("first");

    store.actions.setCurrentSession({ id: "s2", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p2", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
    expect(store.state.queueDraft).toBeUndefined();
    store.actions.invalidateSessionViewRequests();

    store.actions.setCurrentSession({ id: "s1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
    expect(store.state.queueDraft?.queue_items[0]?.text).toBe("first");
  });
  it("rejects stale queue replies after reconnect even when their revision is higher", () => {
    const store = fixture();
    const oldEpoch = store.state.sessionViewEpoch;
    const queue = (revision: number) => ({ revision, queue_items: [], hold: false, sending: false });
    store.actions.setQueueDraft("s1", oldEpoch, queue(100));
    store.actions.invalidateSessionViewRequests();
    expect(store.actions.setQueueDraft("s1", store.state.sessionViewEpoch, queue(1))).toBe(true);
    expect(store.actions.setQueueDraft("s1", oldEpoch, queue(101))).toBe(false);
    expect(store.state.queueDraft?.revision).toBe(1);
  });

});
