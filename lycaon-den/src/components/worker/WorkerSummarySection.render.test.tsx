import { render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { BriefingSnapshot, WorkerTask } from "../../api/types.ts";
import { BRIEFING_DEBOUNCE_MS, resetBriefingLiveForTests, setBriefingClient, setBriefingClockForTests } from "../../briefing/briefing-live.ts";
import { WorkerSummarySection } from "./WorkerSummarySection.tsx";

vi.mock("../../settings/editor/file-summary-settings.ts", () => ({
  fileSummariesEnabled: () => true,
  fileSummariesSettingKnown: () => true,
}));

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
}

const finishedWorker: WorkerTask = {
  id: "00000000-0000-4000-8000-000000000099",
  agent_type: "implementer",
  status: "complete",
  created_at: "2026-11-11T00:00:00Z",
  completed_at: "2026-11-11T00:01:00Z",
  project_id: "proj-1",
};

function makeSnapshot(partial: Partial<BriefingSnapshot>): BriefingSnapshot {
  return {
    profile_id: "worker_result",
    profile_version: "1",
    target_key: finishedWorker.id,
    scope_fingerprint: "fp",
    target: {},
    cache_epoch: "1",
    revision: "1",
    status: "idle",
    pending_sections: [],
    outcome: { state: "succeeded" },
    error_code: "",
    error_message: "",
    ...partial,
  };
}

afterEach(() => resetBriefingLiveForTests());

describe("WorkerSummarySection render", () => {
  it("shows its three labelled blocks waiting while pending, then settled with evidence", async () => {
    const clock = new FakeClock();
    setBriefingClockForTests(clock);
    let nextSnapshot = makeSnapshot({ status: "pending", pending_sections: ["summary"] });
    setBriefingClient(() => ({
      requestBriefing: async () => nextSnapshot,
      getBriefingRecord: async () => {
        throw new Error("unused");
      },
    }));
    const onPresence = vi.fn();
    const pending = render(() => (
      <WorkerSummarySection worker={() => finishedWorker} onSelect={vi.fn()} onPresence={onPresence} />
    ));
    const blocks = () => [...screen.getByTestId("worker-summary").querySelectorAll<HTMLElement>("[data-section]")]
      .map((block) => [block.dataset.section, block.dataset.state ?? block.dataset.source]);

    clock.advance(BRIEFING_DEBOUNCE_MS);
    await waitFor(() => expect(blocks()).toEqual([
      ["Contribution", "waiting"], ["Verification", "waiting"], ["Remaining", "waiting"],
    ]));
    expect(onPresence).toHaveBeenCalledWith(true);
    expect(screen.queryByTestId("briefing-evidence-link")).toBeNull();
    pending.unmount();

    // A fresh view of the same worker reads the settled briefing.
    resetBriefingLiveForTests();
    setBriefingClockForTests(clock);
    nextSnapshot = makeSnapshot({
      result: {
        summary: [{
          section_id: "summary",
          text: "Contribution: Added worker card.\nVerification: Ran `bun test`.\nRemaining: None.",
          manifest_hash: "a".repeat(64),
          source: "summarizer",
          reused: false,
          provider_id: "p",
          model: "m",
          evidence_quality: [],
          created_at: "2026-11-11T00:01:00Z",
        }],
        evidence_ref: { project_id: "proj-1", record_id: "r".repeat(64) },
      },
    });
    setBriefingClient(() => ({
      requestBriefing: async () => nextSnapshot,
      getBriefingRecord: async () => {
        throw new Error("unused");
      },
    }));
    render(() => <WorkerSummarySection worker={() => finishedWorker} onSelect={vi.fn()} onPresence={vi.fn()} />);
    clock.advance(BRIEFING_DEBOUNCE_MS);
    await waitFor(() => expect(blocks()).toEqual([
      ["Contribution", "summarizer"], ["Verification", "summarizer"], ["Remaining", "summarizer"],
    ]));
    expect(screen.queryByTestId("briefing-evidence-link")).not.toBeNull();
  });
});
