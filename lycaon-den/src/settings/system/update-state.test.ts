import { describe, expect, it, vi } from "vitest";
import { createUpdateState } from "./update-state.ts";
import { updateFixture, updateServiceFixture } from "./update-test-fixture.ts";
import type { NativeUpdateState } from "./update-service.ts";
const settle = async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); };
describe("native update projection", () => {
  it("shares one subscription and releases it after the last consumer", async () => {
    const stop = vi.fn(); const subscribe = vi.fn(async () => stop);
    const updates = createUpdateState(updateServiceFixture({ subscribe }));
    const a = updates.mount(); const b = updates.mount(); await settle();
    expect(subscribe).toHaveBeenCalledTimes(1); a(); expect(stop).not.toHaveBeenCalled(); b(); expect(stop).toHaveBeenCalledOnce();
  });
  it("does not replace an event with a delayed initial read", async () => {
    let finish!: (state: NativeUpdateState) => void;
    const updates = createUpdateState(updateServiceFixture({ subscribe: async (handler) => { handler(updateFixture({ revision: 5, installation: "staged" })); return () => {}; }, getState: () => new Promise((resolve) => { finish = resolve; }) }));
    const stop = updates.mount(); await settle(); finish(updateFixture({ revision: 1 })); await settle();
    expect(updates.state()?.installation).toBe("staged"); stop();
  });
  it("accepts a new native process and rejects late replies from the retired process", async () => {
    let deliver!: (state: NativeUpdateState) => void;
    const updates = createUpdateState(updateServiceFixture({ subscribe: async (handler) => { deliver = handler; return () => {}; } }));
    const stop = updates.mount(); await settle();
    deliver(updateFixture({ service_instance_id: "native-2", revision: 0 })); deliver(updateFixture({ revision: 200 }));
    expect(updates.state()?.service_instance_id).toBe("native-2"); stop();
  });
  it("unsubscribes even when unmounted before listener setup finishes", async () => {
    let finish!: (stop: () => void) => void; const unlisten = vi.fn();
    const getState = vi.fn(async () => updateFixture());
    const updates = createUpdateState(updateServiceFixture({ getState, subscribe: () => new Promise((resolve) => { finish = resolve; }) }));
    const stop = updates.mount(); stop(); finish(unlisten); await settle();
    expect(unlisten).toHaveBeenCalledOnce(); expect(getState).not.toHaveBeenCalled();
  });
  it("recovers authoritative native state after a lost command reply", async () => {
    const updates = createUpdateState(updateServiceFixture({ getState: async () => updateFixture({ installation: "staged" }) }));
    await updates.run(async () => { throw new Error("lost reply"); });
    expect(updates.state()?.installation).toBe("staged"); expect(updates.busy()).toBe(false);
  });
});
