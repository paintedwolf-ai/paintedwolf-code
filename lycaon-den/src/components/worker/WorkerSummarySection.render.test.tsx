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

const RESULT_HANDLE = "00000000-0000-4000-8000-0000000000a1";

const finishedWorker: WorkerTask = {
  id: "00000000-0000-4000-8000-000000000099",
  agent_type: "implementer",
  status: "complete",
  result_handle: RESULT_HANDLE,
  created_at: "2026-11-11T00:00:00Z",
  completed_at: "2026-11-11T00:01:00Z",
  project_id: "proj-1",
};

function makeSnapshot(partial: Partial<BriefingSnapshot>): BriefingSnapshot {
  return {
    profile_id: "worker_result",
    profile_version: "1",
    target_key: RESULT_HANDLE,
    scope_fingerprint: "fp",
    target: { result_handle: RESULT_HANDLE },
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
  it("requests briefing by result_handle and renders pending then settled summary blocks", async () => {
    const clock = new FakeClock();
    setBriefingClockForTests(clock);
    const requestBriefing = vi.fn(async () =>
      makeSnapshot({ status: "pending", pending_sections: ["summary"] }),
    );
    setBriefingClient(() => ({
      requestBriefing,
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
    expect(requestBriefing).toHaveBeenCalledWith("proj-1", {
      profile_id: "worker_result",
      target_ref: { result_handle: RESULT_HANDLE },
    });
    expect(onPresence).toHaveBeenCalledWith(true);
    pendingView.unmount();

    resetBriefingLiveForTests();
    setBriefingClockForTests(clock);
    const settledSnapshot = makeSnapshot({
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
      requestBriefing: async () => settledSnapshot,
      getBriefingRecord: async () => {
        throw new Error("unused");
      },
    }));
    render(() => <WorkerSummarySection worker={() => finishedWorker} onSelect={vi.fn()} onPresence={vi.fn()} />);
    clock.advance(BRIEFING_DEBOUNCE_MS);
    await waitFor(() => expect(screen.getByText(/Added worker card\./)).toBeTruthy());
    expect(screen.getByTestId("briefing-evidence-link")).toBeTruthy();
  });

  it("skips briefing when no result_handle is present and supports inline round summaries", async () => {
    const clock = new FakeClock();
    setBriefingClockForTests(clock);
    const requestBriefing = vi.fn(async () =>
      makeSnapshot({
        result: {
          summary: [{
            section_id: "summary",
            text: "Contribution: Earlier round summary.",
            manifest_hash: "b".repeat(64),
            source: "summarizer",
            reused: true,
            provider_id: "p",
            model: "m",
            evidence_quality: [],
            created_at: "2026-11-11T00:01:00Z",
          }],
          evidence_ref: { project_id: "proj-1", record_id: "b".repeat(64) },
        },
      }),
    );
    setBriefingClient(() => ({
      requestBriefing,
      getBriefingRecord: async () => {
        throw new Error("unused");
      },
    }));
    const unsealed = render(() => (
      <WorkerSummarySection
        worker={() => ({ ...finishedWorker, result_handle: undefined })}
        onSelect={vi.fn()}
        onPresence={vi.fn()}
      />
    ));
    clock.advance(BRIEFING_DEBOUNCE_MS);
    expect(requestBriefing).not.toHaveBeenCalled();
    unsealed.unmount();

    const earlierHandle = "00000000-0000-4000-8000-0000000000a0";
    render(() => (
      <WorkerSummarySection
        inline
        resultHandle={() => earlierHandle}
        projectId={() => "proj-1"}
      />
    ));
    clock.advance(BRIEFING_DEBOUNCE_MS);
    await waitFor(() => expect(screen.getByTestId("worker-round-summary")).toBeTruthy());
    expect(requestBriefing).toHaveBeenCalledWith("proj-1", {
      profile_id: "worker_result",
      target_ref: { result_handle: earlierHandle },
    });
    expect(screen.getByText(/Earlier round summary\./)).toBeTruthy();
  });
});
