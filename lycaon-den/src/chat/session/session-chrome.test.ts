import { stubClient } from "../../test/client-fixture.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { valueOf } from "../../store/load-state.ts";
import type { SessionBootstrap } from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import {
  applySessionBootstrapChrome,
  ensureSessionChrome,
  resetSessionChromeHydrationForTests,
  SESSION_CHROME_HYDRATION_CAP,
} from "./session-chrome.ts";

describe("ensureSessionChrome", () => {
  beforeEach(() => resetSessionChromeHydrationForTests());

  it("restores host-managed task and session chrome after a frontend reload", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "session-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "project-1",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const getSessionProgress = vi.fn().mockResolvedValue({
      revision: 3,
      steps: [{ label: "Review the plan", state: "in_progress" }],
    });
    const getSessionFindings = vi.fn().mockResolvedValue({
      revision: 2,
      findings: [{ agent: "review", summary: "Check API" }],
    });
    const getSessionQueue = vi.fn().mockResolvedValue({
      revision: 4,
      queue_items: [{ id: "queued-1", text: "Run checks", submitted_by: "00000000-0000-4000-8000-000000000002", created_at: "t" }],
    });
    const getCoordinatorContext = vi.fn().mockResolvedValue({
      run_id: "run-1",
      current_phase: "review",
    });
    const client = stubClient({
      getSessionProgress,
      getSessionFindings,
      getSessionQueue,
      getCoordinatorContext,
      listSessionBackgroundProcesses: vi.fn().mockResolvedValue([]),
      listSessionPreviews: vi.fn().mockResolvedValue([]),
    });

    await ensureSessionChrome(store, client, "session-1");

    expect(getSessionProgress).toHaveBeenCalledWith("session-1");
    expect(getSessionFindings).toHaveBeenCalledWith("session-1");
    expect(getSessionQueue).toHaveBeenCalledWith("session-1");
    expect(getCoordinatorContext).toHaveBeenCalledWith("session-1");
    expect(store.state.progress?.steps[0]?.label).toBe("Review the plan");
    expect(valueOf(store.state.findings)?.findings[0]?.agent).toBe("review");
    expect(store.state.queueDraft?.queue_items[0]?.id).toBe("queued-1");
    expect(store.state.coordinatorRunContext?.run_id).toBe("run-1");
  });

  it("joins concurrent ensures and skips a remount in the same epoch", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "session-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "project-1",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const getSessionProgress = vi.fn().mockResolvedValue({ revision: 1, steps: [] });
    const client = stubClient({
      getSessionProgress,
      getSessionFindings: vi.fn().mockResolvedValue({ revision: 1, findings: [] }),
      getSessionQueue: vi.fn().mockResolvedValue({ revision: 1, queue_items: [] }),
      getCoordinatorContext: vi.fn().mockResolvedValue(null),
      listSessionBackgroundProcesses: vi.fn().mockResolvedValue([]),
      listSessionPreviews: vi.fn().mockResolvedValue([]),
    });

    await Promise.all([
      ensureSessionChrome(store, client, "session-1"),
      ensureSessionChrome(store, client, "session-1"),
    ]);
    await ensureSessionChrome(store, client, "session-1");

    expect(getSessionProgress).toHaveBeenCalledTimes(1);
  });

  it("retries after a partial hydration failure", async () => {
    const store = createAppStore();
    const getSessionProgress = vi
      .fn()
      .mockRejectedValueOnce(new Error("not ready"))
      .mockResolvedValue({ revision: 1, steps: [] });
    const client = stubClient({
      getSessionProgress,
      getSessionFindings: vi.fn().mockResolvedValue({ revision: 1, findings: [] }),
      getSessionQueue: vi.fn().mockResolvedValue({ revision: 1, queue_items: [] }),
      getCoordinatorContext: vi.fn().mockResolvedValue(null),
      listSessionBackgroundProcesses: vi.fn().mockResolvedValue([]),
      listSessionPreviews: vi.fn().mockResolvedValue([]),
    });

    await ensureSessionChrome(store, client, "session-1");
    await ensureSessionChrome(store, client, "session-1");

    expect(getSessionProgress).toHaveBeenCalledTimes(2);
  });

  it("treats bootstrap projections as hydrated", async () => {
    const store = createAppStore();
    const session: SessionBootstrap["session"] = {
      id: "session-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "project-1",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    };
    const bootstrap: SessionBootstrap = {
      event_cursor: "",
      session,
      transcript: {
        messages: [],
        watermark: 0,
        turn_clocks: {},
        turn_loads: {},
      },
      progress: { revision: 1, steps: [] },
      turn_clock: { session_id: session.id, active_ms: 0, work_ms: 0, running: false },
      findings: { revision: 1, findings: [] },
      queue: { revision: 1, queue_items: [], hold: false, sending: false },
      coordinator: {},
      workers: [],
      checkpoints: [],
      background_outputs: [],
      previews: [],
    };
    applySessionBootstrapChrome(store, bootstrap);
    const getSessionProgress = vi.fn();

    await ensureSessionChrome(
      store,
      stubClient({ getSessionProgress }),
      "session-1",
    );

    expect(getSessionProgress).not.toHaveBeenCalled();
  });

  it("bounds retained session hydrations", async () => {
    const store = createAppStore();
    const getSessionProgress = vi.fn().mockResolvedValue({ revision: 1, steps: [] });
    const client = stubClient({
      getSessionProgress,
      getSessionFindings: vi.fn().mockResolvedValue({ revision: 1, findings: [] }),
      getSessionQueue: vi.fn().mockResolvedValue({ revision: 1, queue_items: [] }),
      getCoordinatorContext: vi.fn().mockResolvedValue(null),
      listSessionBackgroundProcesses: vi.fn().mockResolvedValue([]),
      listSessionPreviews: vi.fn().mockResolvedValue([]),
    });

    for (let index = 0; index <= SESSION_CHROME_HYDRATION_CAP; index += 1) {
      await ensureSessionChrome(store, client, `session-${index}`);
    }
    await ensureSessionChrome(store, client, "session-0");

    expect(getSessionProgress).toHaveBeenCalledTimes(
      SESSION_CHROME_HYDRATION_CAP + 2,
    );
  });
});
