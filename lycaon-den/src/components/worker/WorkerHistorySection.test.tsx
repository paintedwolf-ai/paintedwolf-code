import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { WorkerHistoryResponse, WorkerTask } from "../../api/types.ts";
import {
  BRIEFING_DEBOUNCE_MS,
  resetBriefingLiveForTests,
  setBriefingClient,
  setBriefingClockForTests,
} from "../../briefing/briefing-live.ts";
import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";
import { WorkerHistorySection, type WorkerHistoryFocusTarget } from "./WorkerHistorySection.tsx";

vi.mock("../../settings/editor/file-summary-settings.ts", () => ({
  fileSummariesEnabled: () => true,
  fileSummariesSettingKnown: () => true,
}));

const activeWorker: WorkerTask = {
  id: "job-2",
  worker_record_id: "rec-1",
  worker_name: "reviewer-alpha",
  round: 2,
  result_handle: "00000000-0000-4000-8000-0000000000b2",
  result_disposition: "unaccounted",
  parent_session_id: "sess-1",
  project_id: "proj-1",
  agent_type: "reviewer",
  status: "complete",
  created_at: "2026-11-15T01:05:00Z",
};

afterEach(() => {
  setLycaonClientForTest(null);
  resetBriefingLiveForTests();
});

describe("WorkerHistorySection", () => {
  it("loads bounded history on demand, pages earlier cursors, shows statuses, and exposes zero send controls", async () => {
    const getWorkerHistory = vi
      .fn()
      .mockResolvedValueOnce({
        worker_id: "rec-1",
        name: "reviewer-alpha",
        agent_type: "reviewer",
        state: "delivered",
        before: 2,
        before_round: 2,
        messages: [
          {
            seq: 2,
            sender_role: "coordinator",
            round_ordinal: 2,
            body: "Re-verify token expiry edge cases.",
            delivered: true,
            instruction_status: "applied",
            created_at: "2026-11-15T01:04:00Z",
          },
          {
            seq: 3,
            sender_role: "worker",
            round_ordinal: 2,
            body: "Should clock skew allow 30 seconds?",
            needs_reply: true,
            delivered: false,
            created_at: "2026-11-15T01:05:00Z",
          },
        ],
        rounds: [
          {
            ordinal: 2,
            worker_id: "job-2",
            status: "complete",
            result_handle: "00000000-0000-4000-8000-0000000000b2",
            result_disposition: "unaccounted",
            review_of: "00000000-0000-4000-8000-0000000000a2",
            result: { summary: "Verified round 2 diff." },
            created_at: "2026-11-15T01:05:00Z",
          },
        ],
      } satisfies WorkerHistoryResponse)
      .mockResolvedValueOnce({
        worker_id: "rec-1",
        name: "reviewer-alpha",
        agent_type: "reviewer",
        state: "delivered",
        messages: [
          {
            seq: 1,
            sender_role: "coordinator",
            round_ordinal: 1,
            body: "Queued note before start.",
            delivered: false,
            instruction_status: "not_applied",
            created_at: "2026-11-15T01:00:00Z",
          },
        ],
        rounds: [
          {
            ordinal: 1,
            worker_id: "job-1",
            status: "complete",
            result_handle: "00000000-0000-4000-8000-0000000000b1",
            result_disposition: "shelved",
            shelve_reason: "superseded by round 2",
            review_of: "00000000-0000-4000-8000-0000000000a1",
            result: { summary: "Initial review pass." },
            created_at: "2026-11-15T01:01:00Z",
          },
        ],
      } satisfies WorkerHistoryResponse);

    setLycaonClientForTest({ getWorkerHistory } as never);
    const [focus, setFocus] = createSignal<WorkerHistoryFocusTarget | null>({ messageSeq: 3 });
    const onFocusHandled = vi.fn();

    const view = render(() => (
      <WorkerHistorySection
        worker={() => activeWorker}
        focusTarget={focus}
        onFocusHandled={onFocusHandled}
        onSelect={vi.fn()}
      />
    ));

    await waitFor(() =>
      expect(getWorkerHistory).toHaveBeenCalledWith(
        "proj-1",
        { sessionId: "sess-1", worker: "rec-1", limit: 20 },
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      ),
    );
    await waitFor(() => expect(screen.getAllByTestId("worker-history-message")).toHaveLength(2));
    await waitFor(() => expect(onFocusHandled).toHaveBeenCalled());
    setFocus(null);

    expect(view.container.querySelector("input, textarea, form")).toBeNull();
    expect(screen.getByText("Applied")).toBeTruthy();
    expect(screen.getByText("Needs reply")).toBeTruthy();
    expect(screen.getByText("Unread")).toBeTruthy();
    expect(screen.getByTestId("worker-round-disposition").textContent).toBe("Not accounted for");
    expect(screen.getByTestId("worker-round-reviewed-subject").textContent).toContain(
      "00000000-0000-4000-8000-0000000000a2",
    );

    fireEvent.click(screen.getByTestId("worker-history-load-more"));
    await waitFor(() =>
      expect(getWorkerHistory).toHaveBeenNthCalledWith(2, "proj-1", {
        sessionId: "sess-1",
        worker: "rec-1",
        before: 2,
        beforeRound: 2,
        limit: 20,
      }),
    );
    await waitFor(() => expect(screen.getAllByTestId("worker-history-message")).toHaveLength(3));
    const seqOrder = screen
      .getAllByTestId("worker-history-message")
      .map((el) => Number(el.getAttribute("data-message-seq")));
    expect(seqOrder).toEqual([1, 2, 3]);
    expect(screen.getByText("Not applied")).toBeTruthy();
    expect(screen.getByText("Shelved · superseded by round 2")).toBeTruthy();
    expect(screen.queryByTestId("worker-history-load-more")).toBeNull();
  });

  it("loads an earlier round summary by result_handle only when expanded", async () => {
    let time = 0;
    const timers = new Map<number, { at: number; run: () => void }>();
    let nextId = 0;
    setBriefingClockForTests({
      now: () => time,
      setTimeout: (run, ms) => {
        const id = ++nextId;
        timers.set(id, { at: time + ms, run });
        return id;
      },
      clearTimeout: (h) => timers.delete(h as number),
    });

    const requestBriefing = vi.fn(async () => ({
      profile_id: "worker_result" as const,
      profile_version: "1",
      target_key: "00000000-0000-4000-8000-0000000000b1",
      scope_fingerprint: "fp",
      target: { result_handle: "00000000-0000-4000-8000-0000000000b1" },
      cache_epoch: "1",
      revision: "1",
      status: "idle" as const,
      pending_sections: [],
      outcome: { state: "succeeded" as const },
      error_code: "",
      error_message: "",
      result: {
        summary: [
          {
            section_id: "summary",
            text: "Contribution: Checked round 1 auth invariants.",
            manifest_hash: "c".repeat(64),
            source: "summarizer" as const,
            reused: false,
            provider_id: "p",
            model: "m",
            evidence_quality: [],
            created_at: "2026-11-15T01:01:00Z",
          },
        ],
        evidence_ref: { project_id: "proj-1", record_id: "e".repeat(64) },
      },
    }));
    setBriefingClient(() => ({
      requestBriefing,
      getBriefingRecord: async () => {
        throw new Error("unused");
      },
    }));
    setLycaonClientForTest({
      getWorkerHistory: vi.fn().mockResolvedValue({
        worker_id: "rec-1",
        name: "reviewer-alpha",
        agent_type: "reviewer",
        state: "delivered",
        messages: [],
        rounds: [
          {
            ordinal: 1,
            worker_id: "job-1",
            status: "complete",
            result_handle: "00000000-0000-4000-8000-0000000000b1",
            result_disposition: "landed",
            created_at: "2026-11-15T01:01:00Z",
          },
        ],
      } satisfies WorkerHistoryResponse),
    } as never);

    render(() => <WorkerHistorySection worker={() => activeWorker} onSelect={vi.fn()} />);
    const toggle = await screen.findByTestId("worker-round-summary-toggle-1");
    expect(requestBriefing).not.toHaveBeenCalled();

    fireEvent.click(toggle);
    time += BRIEFING_DEBOUNCE_MS;
    for (const [id, timer] of [...timers]) {
      if (timer.at <= time) {
        timers.delete(id);
        timer.run();
      }
    }
    await waitFor(() =>
      expect(requestBriefing).toHaveBeenCalledWith("proj-1", {
        profile_id: "worker_result",
        target_ref: { result_handle: "00000000-0000-4000-8000-0000000000b1" },
      }),
    );
    expect(await screen.findByText(/Checked round 1 auth invariants\./)).toBeTruthy();
  });
});
