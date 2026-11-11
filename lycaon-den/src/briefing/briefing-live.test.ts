import { createRoot } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import type { BriefingRequest, BriefingSnapshot } from "../api/types.ts";
import type { BriefingsClient } from "../api/http-capabilities/briefings.ts";
import {
  BRIEFING_DEBOUNCE_MS, BRIEFING_INACTIVE_LIMIT, applyBriefingEvent, briefingEntryCountForTests, requestBriefing, requestBriefingResync,
  resetBriefingLiveForTests, setBriefingClient, setBriefingClockForTests, useBriefing, type BriefingTarget,
} from "./briefing-live.ts";

class FakeClock {
  time = 0;
  private timers = new Map<number, { at: number; run: () => void }>();
  private next = 0;
  now = () => this.time;
  setTimeout = (run: () => void, ms: number) => {
    const id = ++this.next;
    this.timers.set(id, { at: this.time + ms, run });
    return id;
  };
  clearTimeout = (handle: unknown) => {
    this.timers.delete(handle as number);
  };
  advance(ms: number) {
    this.time += ms;
    for (const [id, timer] of [...this.timers]) {
      if (timer.at <= this.time) {
        this.timers.delete(id);
        timer.run();
      }
    }
  }
  pending() {
    return this.timers.size;
  }
}

function snapshot(over: Partial<BriefingSnapshot> = {}): BriefingSnapshot {
  return {
    profile_id: "tool_activity", profile_version: "1", target_key: "card", scope_fingerprint: "card", target: {},
    cache_epoch: "1", revision: "1", status: "idle", pending_sections: [], outcome: { state: "unknown" },
    error_code: "", error_message: "", ...over,
  };
}

function stubClient(replies: BriefingSnapshot[]): BriefingsClient & { calls: BriefingRequest[] } {
  const calls: BriefingRequest[] = [];
  return {
    calls,
    requestBriefing: async (_project, req) => {
      calls.push(req);
      return replies.shift() ?? snapshot();
    },
    getBriefingRecord: async () => {
      throw new Error("unused");
    },
  };
}

const target: BriefingTarget = { profileId: "tool_activity", ref: { session_id: "s", tool_call_ids: ["t1"] } };
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

function watch(projectId: string, wanted: BriefingTarget) {
  let dispose = () => {};
  const view = createRoot((done) => {
    dispose = done;
    return useBriefing(() => projectId, () => wanted);
  });
  return { view, dispose };
}

afterEach(() => resetBriefingLiveForTests());

describe("briefing live store", () => {
  it("debounces automatic requests into one", async () => {
    const clock = new FakeClock();
    setBriefingClockForTests(clock);
    const client = stubClient([snapshot({ status: "idle", result: { summary: [], evidence_ref: { project_id: "p", record_id: "r" } } })]);
    setBriefingClient(() => client);
    for (let i = 0; i < 5; i++) requestBriefing("p", target);
    clock.advance(BRIEFING_DEBOUNCE_MS - 1);
    expect(client.calls).toHaveLength(0);
    clock.advance(1);
    await settle();
    expect(client.calls).toHaveLength(1);
    expect(client.calls[0]!.trigger).toBe("automatic");
  });

  it("keeps the newest epoch and revision whatever order they arrive in", async () => {
    const clock = new FakeClock();
    setBriefingClockForTests(clock);
    setBriefingClient(() => stubClient([snapshot({ revision: "9" })]));
    const { view, dispose } = watch("p", target);
    clock.advance(BRIEFING_DEBOUNCE_MS);
    await settle();
    applyBriefingEvent("p", { kind: "snapshot", snapshot: snapshot({ revision: "10" }) });
    applyBriefingEvent("p", { kind: "snapshot", snapshot: snapshot({ revision: "2" }) });
    expect(view().snapshot?.revision).toBe("10");
    applyBriefingEvent("p", { kind: "snapshot", snapshot: snapshot({ cache_epoch: "2", revision: "1" }) });
    expect(view().snapshot?.cache_epoch).toBe("2");
    dispose();
  });

  it("reconciles once at the deadline and then stops waiting", async () => {
    const clock = new FakeClock();
    setBriefingClockForTests(clock);
    const pending = snapshot({ status: "pending", pending_sections: ["purpose"], reconcile_after: new Date(5000).toISOString() });
    const client = stubClient([pending, { ...pending, revision: "2" }]);
    setBriefingClient(() => client);
    const { view, dispose } = watch("p", target);
    clock.advance(BRIEFING_DEBOUNCE_MS);
    await settle();
    expect(view().stalled).toBe(false);
    clock.advance(5000);
    await settle();
    expect(client.calls).toHaveLength(2);
    expect(view().stalled).toBe(true);
    expect(clock.pending()).toBe(0);
    dispose();
  });

  it("drops results from epochs a reset cleared", async () => {
    const clock = new FakeClock();
    setBriefingClockForTests(clock);
    setBriefingClient(() => stubClient([snapshot()]));
    const { view, dispose } = watch("p", target);
    clock.advance(BRIEFING_DEBOUNCE_MS);
    await settle();
    applyBriefingEvent("p", { kind: "reset", profile_ids: ["tool_activity"], cache_epoch: "2" });
    expect(view().snapshot).toBeNull();
    applyBriefingEvent("p", { kind: "snapshot", snapshot: snapshot({ revision: "5" }) });
    expect(view().snapshot).toBeNull();
    dispose();
  });

  it("re-asks only watched targets after reconnect", async () => {
    const clock = new FakeClock();
    setBriefingClockForTests(clock);
    const client = stubClient([]);
    setBriefingClient(() => client);
    const watched = watch("p", target);
    requestBriefing("p", { profileId: "tool_activity", ref: { session_id: "s", tool_call_ids: ["other"] } });
    clock.advance(BRIEFING_DEBOUNCE_MS);
    await settle();
    expect(client.calls).toHaveLength(2);
    requestBriefingResync();
    clock.advance(BRIEFING_DEBOUNCE_MS);
    await settle();
    expect(client.calls).toHaveLength(3);
    watched.dispose();
  });

  it("bounds inactive targets", () => {
    setBriefingClockForTests(new FakeClock());
    setBriefingClient(() => null);
    for (let i = 0; i <= BRIEFING_INACTIVE_LIMIT + 10; i++) requestBriefing("p", { profileId: "tool_activity", ref: { n: i } });
    expect(briefingEntryCountForTests()).toBe(BRIEFING_INACTIVE_LIMIT);
    const watched = watch("p", { profileId: "tool_activity", ref: { n: "watched" } });
    for (let i = 0; i < 20; i++) requestBriefing("p", { profileId: "tool_activity", ref: { m: i } });
    expect(briefingEntryCountForTests()).toBe(BRIEFING_INACTIVE_LIMIT + 1);
    watched.dispose();
  });

  it("asks once for a target however many views watch it", async () => {
    const clock = new FakeClock();
    setBriefingClockForTests(clock);
    const client = stubClient([snapshot({ status: "idle" })]);
    setBriefingClient(() => client);
    for (let i = 0; i < 5; i++) {
      const view = watch("p", target);
      clock.advance(BRIEFING_DEBOUNCE_MS);
      await settle();
      view.dispose();
    }
    expect(client.calls).toHaveLength(1);
  });

  it("asks nothing for a view that closes before the debounce", async () => {
    const clock = new FakeClock();
    setBriefingClockForTests(clock);
    const client = stubClient([]);
    setBriefingClient(() => client);
    for (let i = 0; i < 5; i++) {
      const view = watch("p", { profileId: "source_step", ref: { target_key: `e${i}` } });
      clock.advance(BRIEFING_DEBOUNCE_MS - 1);
      view.dispose();
    }
    clock.advance(BRIEFING_DEBOUNCE_MS);
    await settle();
    expect(client.calls).toHaveLength(0);
  });
});
