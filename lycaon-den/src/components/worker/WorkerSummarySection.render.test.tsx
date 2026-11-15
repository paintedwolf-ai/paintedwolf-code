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
  it("renders waiting blocks while pending and labelled summary blocks with evidence once settled", async () => {
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
    const pendingView = render(() => (
      <WorkerSummarySection worker={() => finishedWorker} onSelect={vi.fn()} onPresence={onPresence} />
    ));
    clock.advance(BRIEFING_DEBOUNCE_MS);
    await waitFor(() => expect(pendingView.getByTestId("worker-summary")).toBeTruthy());
    expect(onPresence).toHaveBeenCalledWith(true);
    pendingView.unmount();

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
    await waitFor(() => expect(screen.getByText(/Added worker card\./)).toBeTruthy());
    expect(screen.getByTestId("briefing-evidence-link")).toBeTruthy();
  });
});
