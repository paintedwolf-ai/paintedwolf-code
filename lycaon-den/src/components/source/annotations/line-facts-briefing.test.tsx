import { render } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { BriefingRequest, BriefingSnapshot } from "../../../api/types.ts";
import { BRIEFING_DEBOUNCE_MS, resetBriefingLiveForTests, setBriefingClient, setBriefingClockForTests } from "../../../briefing/briefing-live.ts";
import { mergeAttributionIntervals } from "./knowing-gutter-model.ts";
import { LineFactsBriefing } from "./line-facts-briefing.tsx";

class FakeClock {
  time = 0;
  private timers = new Map<number, { at: number; run: () => void }>();
  private next = 0;
  now = () => this.time;
  setTimeout = (run: () => void, ms: number) => { const id = ++this.next; this.timers.set(id, { at: this.time + ms, run }); return id; };
  clearTimeout = (handle: unknown) => { this.timers.delete(handle as number); };
  advance(ms: number) {
    this.time += ms;
    for (const [id, timer] of [...this.timers]) if (timer.at <= this.time) { this.timers.delete(id); timer.run(); }
  }
}

vi.mock("../../../settings/editor/file-summary-settings.ts", () => ({ fileSummariesEnabled: () => true, fileSummariesSettingKnown: () => true }));

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

function snapshot(key: string): BriefingSnapshot {
  return { profile_id: "source_step", profile_version: "1", target_key: key, scope_fingerprint: "f", target: {}, cache_epoch: "1", revision: "1",
    status: "idle", pending_sections: [], outcome: { state: "unknown" }, error_code: "", error_message: "" };
}

function setup() {
  const clock = new FakeClock();
  setBriefingClockForTests(clock);
  const calls: BriefingRequest[] = [];
  setBriefingClient(() => ({
    requestBriefing: async (_p, req) => { calls.push(req); return snapshot(String((req.target_ref as { target_key: string }).target_key)); },
    getBriefingRecord: async () => { throw new Error("unused"); },
  }));
  return { clock, calls };
}

function hover(effectId: string, line: number) {
  return render(() => <LineFactsBriefing projectId="p" effectId={effectId} line={line} onOpenEvidence={vi.fn()} returnFocus={vi.fn()} />);
}

afterEach(() => resetBriefingLiveForTests());

describe("gutter briefing cost", () => {
  it("asks once for a change however many of its lines are hovered", async () => {
    const { clock, calls } = setup();
    for (let line = 1; line <= 20; line++) {
      const card = hover("effect-1", line);
      clock.advance(BRIEFING_DEBOUNCE_MS);
      await settle();
      card.unmount();
    }
    expect(calls).toHaveLength(1);
  });

  it("asks nothing while the pointer sweeps across changes", async () => {
    const { clock, calls } = setup();
    for (let i = 0; i < 20; i++) {
      const card = hover(`effect-${i}`, i + 1);
      clock.advance(BRIEFING_DEBOUNCE_MS - 1);
      card.unmount();
    }
    clock.advance(BRIEFING_DEBOUNCE_MS);
    await settle();
    expect(calls).toHaveLength(0);
  });

  it("names the change behind each line from its attribution", () => {
    const marks = mergeAttributionIntervals([
      { start_line: 1, end_line: 3, turn: 2, session_id: "s", tool_call_id: "c", recorded_at: "2026-11-11T00:00:00Z", effect_id: "effect-1" },
    ]);
    expect(marks[0]?.contributors[0]?.effectId).toBe("effect-1");
  });
});
