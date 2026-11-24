// @vitest-environment jsdom
import { createSignal } from "solid-js";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import { LycaonApiError } from "../../api/http.ts";
import type {
  ActionDescriptor,
  AgentWatch,
  StandingWatch,
  WatchCondition,
  WatchRun,
} from "../../api/types.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { ChatTabChromeProvider } from "../../chat/composer/chat-tab-chrome.tsx";
import { projectScope, noticeScopeKey } from "../../notices/notice-scope.ts";
import { createNoticeStore } from "../../notices/notice-store.ts";
import {
  openRunningStage,
  registerOpenRunningStageSink,
  resetOpenRunningForTests,
} from "../../platform/navigation/open-running.ts";
import {
  applyProjectBackgroundSnapshot,
  resetProjectBackgroundForTests,
} from "../../project/project-background-store.ts";
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import { createAppStore } from "../../store/app-state.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { mountStandingWatchNotices } from "../../watches/mount-standing-watch-notices.ts";
import {
  deleteProjectStandingWatchAction,
  resumeProjectStandingWatchAction,
  runProjectStandingWatchNowAction,
  setSessionWatchDeliveryAction,
  updateProjectStandingWatchAction,
} from "../../watches/watches-actions.ts";
import {
  applyWatchUpdatedEvent,
  invalidateWatchesForReconnect,
  projectWatchesFor,
  refreshProjectWatches,
  refreshSessionWatches,
  retainProjectWatchesProject,
  sessionWatchesFor,
  upsertAgentWatchInStore,
  upsertStandingWatchInStore,
} from "../../watches/watches-store.ts";
import { ActionCard } from "../action/ActionCard.tsx";
import { ChatTabRail } from "../nav/ChatTabRail.tsx";
import { ChatTopChromeStack } from "../nav/ChatTopChromeStack.tsx";
import { ProjectContextZone } from "../nav/ProjectContextZone.tsx";
import { ProjectRunningView } from "../project/ProjectRunningView.tsx";
import { ActivityLiveSection } from "./ActivityLiveSection.tsx";
import { StandingWatchConditionFields } from "./StandingWatchConditionFields.tsx";
import { createWatchProposalSlot } from "./WatchProposalStack.tsx";

const PROJ_ID = "00000000-0000-4000-8000-000000000001";
const SESS_ID = "00000000-0000-4000-8000-000000000002";
const OWNER_ID = "00000000-0000-4000-8000-000000000003";

function makeAgentWatch(overrides: Partial<AgentWatch> = {}): AgentWatch {
  return {
    watch_id: "00000000-0000-4000-8000-000000000101",
    handle: "watch-1",
    owner_kind: "session",
    project_id: PROJ_ID,
    session_id: SESS_ID,
    conditions: [{ kind: "timer", every_minutes: 90 }],
    condition_summary: "Every 90 minutes",
    delivery: "collect",
    repeat: true,
    derived: false,
    keepable: true,
    state: "live",
    revision: 1,
    expires_at: "2026-05-01T00:00:00Z",
    created_at: "2026-04-20T10:00:00Z",
    updated_at: "2026-04-20T10:00:00Z",
    ...overrides,
  };
}

function makeStandingWatch(
  overrides: Partial<StandingWatch> = {},
): StandingWatch {
  return {
    watch_id: "00000000-0000-4000-8000-000000000201",
    project_id: PROJ_ID,
    owner_person_id: OWNER_ID,
    name: "Release check",
    prompt: "Verify release branch tests\nReport failures",
    conditions: [{ kind: "timer", every_minutes: 10_080 }],
    condition_summary: "Every week",
    min_gap_minutes: 15,
    target: { kind: "new_chat" },
    workflow_id: "default",
    model_id: "mock:fast",
    run_spend_limit_nano_usd: 2_000_000_000,
    allow_unpriced: false,
    daily_run_cap: 8,
    time_zone: "UTC",
    state: "enabled",
    next_run_at: "2026-04-27T10:00:00Z",
    last_outcome: "completed",
    revision: 1,
    created_at: "2026-04-20T10:00:00Z",
    updated_at: "2026-04-20T10:00:00Z",
    ...overrides,
  };
}

describe("SW10 watches, proposals, transcript controls, and Activity", () => {
  beforeEach(() => {
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    retainProjectWatchesProject(null);
    resetProjectBackgroundForTests();
    resetOpenRunningForTests();
  });

  it("stacks 4 proposals (2 oldest + overflow card) alongside an active session slot, supports Hide watermark reset, conflict Review changes, Keep, Edit and keep, and Dismiss", async () => {
    const p1 = makeAgentWatch({
      watch_id: "00000000-0000-4000-8000-000000000101",
      handle: "watch-1",
      created_at: "2026-04-20T10:01:00Z",
      standing_draft: {
        name: "First check",
        prompt: "Check main CI\nSecond line",
      },
    });
    const p2 = makeAgentWatch({
      watch_id: "00000000-0000-4000-8000-000000000102",
      handle: "watch-2",
      created_at: "2026-04-20T10:02:00Z",
      conditions: [{ kind: "timer", every_minutes: 10_080 }],
      standing_draft: {
        name: "Weekly audit",
        prompt: "Audit dependencies",
        replaces: "00000000-0000-4000-8000-000000000201",
      },
    });
    const p3 = makeAgentWatch({
      watch_id: "00000000-0000-4000-8000-000000000103",
      handle: "watch-3",
      created_at: "2026-04-20T10:03:00Z",
      standing_draft: { name: "Third check", prompt: "Check staging" },
    });
    const p4 = makeAgentWatch({
      watch_id: "00000000-0000-4000-8000-000000000104",
      handle: "watch-4",
      created_at: "2026-04-20T10:04:00Z",
      standing_draft: { name: "Fourth check", prompt: "Check canary" },
    });

    const existing = makeStandingWatch({ revision: 2 });
    await refreshProjectWatches(
      stubClient({
        listProjectWatches: vi.fn(async () => ({
          watches: [
            {
              watch_id: existing.watch_id,
              owner_kind: "person" as const,
              project_id: PROJ_ID,
              revision: 2,
              standing_watch: existing,
            },
          ],
          daily_spend_cap_nano_usd: 16_000_000_000,
          has_unpriced_standing_watch: false,
          enabled_standing_total: 1,
        })),
      }),
      PROJ_ID,
    );
    await refreshSessionWatches(
      stubClient({
        listWatches: vi.fn(async () => ({ watches: [p1, p2, p3, p4] })),
      }),
      SESS_ID,
    );

    let keepAttempt = 0;
    const keepWatch = vi.fn(async (_sid: string, handle: string) => {
      keepAttempt += 1;
      if (handle === "watch-2" && keepAttempt === 1) {
        throw new LycaonApiError(
          "Watch changed since loaded.",
          409,
          "watch_revision_conflict",
          { title: "Watch changed" },
        );
      }
      return {
        standing_watch: makeStandingWatch({
          watch_id: "00000000-0000-4000-8000-000000000299",
          name: "First check",
        }),
        receipt: { handle, state: "terminal" as const, terminal_outcome: "kept" as const },
      };
    });
    const cancelWatch = vi.fn(async (_sid: string, handle: string) => {
      const base = sessionWatchesFor(SESS_ID).byHandle.get(handle) ?? p1;
      return {
        watch: {
          ...base,
          state: "terminal" as const,
          terminal_outcome: "dismissed" as const,
          revision: 5,
        },
        receipt: { handle, state: "terminal" as const, terminal_outcome: "dismissed" as const },
      };
    });

    const client = stubClient({
      keepWatch,
      cancelWatch,
      listWatches: vi.fn(async () => ({ watches: [p1, p2, p3, p4] })),
      listProjectWatches: vi.fn(async () => ({
        watches: [],
        daily_spend_cap_nano_usd: 0,
        has_unpriced_standing_watch: false,
        enabled_standing_total: 0,
      })),
    });

    const openedRequests: unknown[] = [];
    registerOpenRunningStageSink((req) => openedRequests.push(req));

    render(() => {
      const proposal = createWatchProposalSlot({
        sessionId: () => SESS_ID,
        projectId: () => PROJ_ID,
        client: () => client,
        announceLive: () => true,
      });
      return (
        <div>
          <div data-testid="pending-approval-banner">Approval waiting</div>
          <ChatTopChromeStack
            notifications={{
              present: () => true,
              children: () => <div>Shared notices</div>,
            }}
            proposal={proposal}
            verify={{
              present: () => true,
              children: () => <div>Verify suggestion</div>,
            }}
          />
        </div>
      );
    });

    // Pending approval coexists unaffected; two oldest proposal cards + overflow card render.
    expect(screen.getByTestId("pending-approval-banner")).toBeTruthy();
    expect(screen.getByTestId("watch-proposal-card-watch-1")).toBeTruthy();
    expect(screen.getByTestId("watch-proposal-card-watch-2")).toBeTruthy();
    expect(screen.queryByTestId("watch-proposal-card-watch-3")).toBeNull();
    expect(screen.getByTestId("watch-proposal-overflow-card").textContent).toContain(
      "2 more proposals",
    );

    // Largest-unit wording on cards 1 & 2.
    expect(screen.getByTestId("watch-proposal-when-watch-1").textContent).toContain(
      "Every 90 minutes · New chat each run · Up to $16 a day",
    );
    expect(screen.getByTestId("watch-proposal-when-watch-2").textContent).toContain(
      "Every week · New chat each run · Up to $16 a day",
    );

    // Hide overflow card -> hides only overflow card, proposals stay in store.
    const hideBtn = screen.getByTestId("watch-proposal-overflow-hide");
    expect(hideBtn.getAttribute("aria-label")).toBe("Hide");
    hideBtn.focus();
    fireEvent.click(hideBtn);
    expect(screen.queryByTestId("watch-proposal-overflow-card")).toBeNull();
    expect(sessionWatchesFor(SESS_ID).proposals.length).toBe(4);

    // Arrival of a 5th proposal resets overflow hide automatically.
    const p5 = makeAgentWatch({
      watch_id: "00000000-0000-4000-8000-000000000105",
      handle: "watch-5",
      created_at: "2026-04-20T10:05:00Z",
      standing_draft: { name: "Fifth check", prompt: "Check prod" },
    });
    upsertAgentWatchInStore(p5);
    expect(screen.getByTestId("watch-proposal-overflow-card").textContent).toContain(
      "3 more proposals",
    );

    // Revision conflict on watch-2 Keep switches line 3 and turns primary action into Review changes.
    fireEvent.click(screen.getByTestId("watch-proposal-keep-watch-2"));
    await waitFor(() => {
      expect(
        screen.getByTestId("watch-proposal-conflict-watch-2").textContent,
      ).toBe("This watch changed since the proposal");
    });
    const reviewBtn = screen.getByTestId("watch-proposal-review-watch-2");
    expect(reviewBtn.textContent).toBe("Review changes");
    fireEvent.click(reviewBtn);
    expect(openedRequests[openedRequests.length - 1]).toMatchObject({
      projectId: PROJ_ID,
      prefillFromWatch: { sessionId: SESS_ID, handle: "watch-2", reviewChanges: true },
    });

    // Keep watch-1 settles card; Dismiss watch-2 cancels it.
    fireEvent.click(screen.getByTestId("watch-proposal-keep-watch-1"));
    await waitFor(() => {
      expect(screen.queryByTestId("watch-proposal-card-watch-1")).toBeNull();
    });
    const dismissBtn = screen.getByTestId("watch-proposal-dismiss-watch-2");
    expect(dismissBtn.getAttribute("aria-label")).toBe("Dismiss proposal");
    fireEvent.click(dismissBtn);
    await waitFor(() => {
      expect(screen.queryByTestId("watch-proposal-card-watch-2")).toBeNull();
    });
  });

  it("keeps collapsed transcript rows clean and exposes delivery, Cancel watch, Keep, and final receipt recovery inside expanded details alongside distinct Stop command", async () => {
    const derived = makeAgentWatch({
      handle: "watch-7",
      derived: true,
      keepable: false,
      repeat: false,
      source_process_id: "cmd-1",
      conditions: [{ kind: "process_done", process_id: "cmd-1" }],
      condition_summary: "When command cmd-1 finishes",
    });
    await refreshSessionWatches(
      stubClient({ listWatches: vi.fn(async () => ({ watches: [derived] })) }),
      SESS_ID,
    );

    const updateWatchDelivery = vi.fn(async () => ({
      ...derived,
      delivery: "wake" as const,
      revision: 2,
    }));
    const cancelWatch = vi.fn(async () => ({
      watch: {
        ...derived,
        state: "terminal" as const,
        terminal_outcome: "canceled" as const,
        revision: 3,
      },
      receipt: {
        handle: "watch-7",
        state: "terminal" as const,
        terminal_outcome: "canceled" as const,
      },
    }));
    const getWatch = vi.fn(async () => ({
      ...derived,
      state: "terminal" as const,
      terminal_outcome: "canceled" as const,
      final_receipt: {
        handle: "watch-7",
        state: "terminal" as const,
        terminal_outcome: "canceled" as const,
      },
      revision: 4,
    }));

    const client = stubClient({
      listWatches: vi.fn(async () => ({ watches: [derived] })),
      updateWatchDelivery,
      cancelWatch,
      getWatch,
      getSessionBackgroundProcessOutput: vi.fn(async () => ({
        process_id: "cmd-1",
        chunks: [],
        truncated: false,
        running: true,
      })),
    });

    const cmdPart: ToolPartView = {
      id: "m1:tc1",
      toolCallId: "tc1",
      assistantMessageId: "m1",
      messageId: "m2",
      tool: "command",
      kind: "command",
      status: "running",
      process: { handle: "cmd-1", running: true },
      output: JSON.stringify({ derived_watches: [{ handle: "watch-7" }] }),
    };
    const descriptor: ActionDescriptor = {
      label: "Run command",
      status: "running",
      ran: { noun: "command", text: "{ npm run build; } &" },
      row: [],
      facts: [],
      links: [],
    };

    render(() => (
      <ActionCard
        part={cmdPart}
        action={descriptor}
        layout="chat"
        sessionId={SESS_ID}
        projectId={PROJ_ID}
        client={client}
      />
    ));

    // Collapsed row has zero watch controls in Tab order (A7).
    expect(screen.queryByTestId("action-watch-controls")).toBeNull();
    expect(screen.queryByTestId("action-watch-cancel-watch-7")).toBeNull();

    // Expand tool row by keyboard/click -> exposes Stop command AND distinct Cancel watch.
    const disclosure = screen.getByRole("button");
    disclosure.focus();
    fireEvent.click(disclosure);

    expect(screen.getByTestId("background-process-stop").textContent).toBe("Stop");
    expect(screen.getByTestId("action-watch-cancel-watch-7").textContent).toBe(
      "Cancel watch",
    );

    // Switch delivery to wake, then cancel watch, reconnect, and inspect to recover final receipt.
    fireEvent.click(screen.getByTestId("action-watch-delivery-watch-7"));
    await waitFor(() => {
      expect(screen.getByTestId("action-watch-state-watch-7").textContent).toContain(
        "Live (wake)",
      );
    });

    fireEvent.click(screen.getByTestId("action-watch-cancel-watch-7"));
    await waitFor(() => {
      expect(screen.getByTestId("action-watch-receipt-watch-7").textContent).toContain(
        "canceled",
      );
    });

    invalidateWatchesForReconnect();
    fireEvent.click(screen.getByTestId("action-watch-inspect-watch-7"));
    await waitFor(() => {
      expect(getWatch).toHaveBeenCalledWith(SESS_ID, "watch-7");
      expect(screen.getByTestId("action-watch-receipt-watch-7").textContent).toContain(
        "canceled",
      );
    });
  });

  it("supports standing watch CRUD, queued runs, run history, chat header link, project notices, and cross-view settlement", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const noticeStore = createNoticeStore();
    const stopNotices = mountStandingWatchNotices({ notices: noticeStore });

    applyProjectBackgroundSnapshot(PROJ_ID, { revision: 1, processes: [] });
    await refreshProjectWatches(
      stubClient({
        listProjectWatches: vi.fn(async () => ({
          watches: [],
          daily_spend_cap_nano_usd: 0,
          has_unpriced_standing_watch: false,
          enabled_standing_total: 0,
        })),
      }),
      PROJ_ID,
    );

    let currentStanding = makeStandingWatch();
    const queuedRun: WatchRun = {
      run_id: "00000000-0000-4000-8000-000000000301",
      watch_id: currentStanding.watch_id,
      watch_name: currentStanding.name,
      project_id: PROJ_ID,
      status: "queued",
      scheduled_at: "2026-04-20T12:00:00Z",
      missed_count: 2,
      spend_limit_nano_usd: 2_000_000_000,
      allow_unpriced: false,
      created_at: "2026-04-20T12:00:00Z",
    };
    const completedRun: WatchRun = {
      ...queuedRun,
      run_id: "00000000-0000-4000-8000-000000000302",
      status: "completed",
      session_id: SESS_ID,
      ended_at: new Date().toISOString(),
    };

    const createStandingWatch = vi.fn(async (_pid, req) => {
      currentStanding = makeStandingWatch({
        name: req.name,
        prompt: req.prompt,
        conditions: req.conditions,
        time_zone: req.time_zone ?? "UTC",
        recent_runs: [queuedRun, completedRun],
      });
      return currentStanding;
    });
    const pauseStandingWatch = vi.fn(async () => {
      currentStanding = {
        ...currentStanding,
        state: "paused",
        state_reason: "consecutive_failures",
        revision: currentStanding.revision + 1,
      };
      return currentStanding;
    });
    const runStandingWatchNow = vi.fn(async () => queuedRun);
    const deleteStandingWatch = vi.fn(async () => undefined);
    const listWatchRuns = vi.fn(async () => ({ runs: [completedRun] }));

    const client = stubClient({
      createStandingWatch,
      pauseStandingWatch,
      runStandingWatchNow,
      deleteStandingWatch,
      listWatchRuns,
      listProjectWatches: vi.fn(async () => ({
        watches: [
          {
            watch_id: currentStanding.watch_id,
            owner_kind: "person" as const,
            project_id: PROJ_ID,
            revision: currentStanding.revision,
            standing_watch: currentStanding,
          },
        ],
        daily_spend_cap_nano_usd: 16_000_000_000,
        has_unpriced_standing_watch: false,
        enabled_standing_total: 1,
      })),
    });

    appStore.actions.setCurrentSession({
      id: SESS_ID,
      root_session_id: SESS_ID,
      project_id: PROJ_ID,
      owner_person_id: OWNER_ID,
      mode: "agent",
      started: true,
      status: "idle",
      posture: "build",
      activity_at: "2026-04-20T12:00:00Z",
      created_at: "2026-04-20T12:00:00Z",
      updated_at: "2026-04-20T12:00:00Z",
      ui: {
        standing_watch: {
          watch_id: currentStanding.watch_id,
          watch_name: "Nightly suite",
          run_id: completedRun.run_id,
          deleted: false,
        },
      },
    });

    const onOpenSession = vi.fn();
    render(() => (
      <div style={{ width: "360px" }}>
        <ProjectContextZone
          projectId={PROJ_ID}
          onOpenStage={() => undefined}
          isStageActive={(id) => id === "running"}
          isStageAvailable={() => true}
        />
        <ChatTabChromeProvider sessionKey={SESS_ID}>
          <ChatTabRail
            appStore={appStore}
            sessionId={SESS_ID}
            onOpenWorker={() => undefined}
            worklogOpen={false}
            onOpenWorklog={() => undefined}
          />
        </ChatTabChromeProvider>
        <ProjectRunningView
          projectId={PROJ_ID}
          appStore={appStore}
          client={client}
          onOpenSession={onOpenSession}
        />
      </div>
    ));

    // Empty state offers New standing watch.
    expect(screen.getByTestId("project-running-empty").textContent).toBe(
      "Nothing running or watching",
    );
    fireEvent.click(screen.getByTestId("activity-empty-new-standing-watch"));

    // Fill editor and verify live 3-run preview in time_zone.
    fireEvent.input(screen.getByTestId("standing-watch-name-input"), {
      target: { value: "Nightly suite" },
    });
    fireEvent.input(screen.getByTestId("standing-watch-prompt-input"), {
      target: { value: "Run full test suite" },
    });
    fireEvent.input(screen.getByTestId("standing-watch-timezone-input"), {
      target: { value: "America/New_York" },
    });
    expect(screen.getByTestId("standing-watch-next-runs").textContent).toContain(
      "America/New_York",
    );

    fireEvent.submit(screen.getByTestId("standing-watch-editor"));
    await waitFor(() => {
      expect(
        screen.getByTestId(`standing-watch-name-${currentStanding.watch_id}`)
          .textContent,
      ).toBe("Nightly suite");
    });

    // Header spend line, queued run in Live, and run history with missed count + Open chat.
    expect(screen.getByTestId("activity-daily-spend-line").textContent).toBe(
      "Up to $16 a day across 1 standing watch",
    );
    expect(
      screen.getByTestId(`activity-queued-run-${queuedRun.run_id}`).textContent,
    ).toContain("Queued · Release check");
    expect(
      screen.getByTestId(`standing-watch-history-missed-${completedRun.run_id}`)
        .textContent,
    ).toBe("Missed 2");
    fireEvent.click(
      screen.getByTestId(`standing-watch-history-chat-${completedRun.run_id}`),
    );
    expect(onOpenSession).toHaveBeenCalledWith(SESS_ID);

    // Chat header links to the standing watch while it exists.
    expect(screen.getByTestId("chat-header-standing-watch-link").textContent).toBe(
      "Nightly suite",
    );

    // Pause with consecutive_failures -> raises project notice and attention dot on Activity.
    fireEvent.click(
      screen.getByTestId(`standing-watch-menu-${currentStanding.watch_id}-trigger`),
    );
    fireEvent.click(
      screen.getByTestId(`standing-watch-pause-${currentStanding.watch_id}`),
    );
    await waitFor(() => {
      expect(screen.getByTestId("project-context-attention-running")).toBeTruthy();
    });
    const scopeKey = noticeScopeKey(projectScope(PROJ_ID));
    expect(
      noticeStore
        .index()
        .get(scopeKey)
        ?.some((n) => n.code === "standing_watch_paused"),
    ).toBe(true);

    // SSE delete settles the list, clears notice and dot, and updates the chat header.
    applyWatchUpdatedEvent({
      action: "deleted",
      watch_id: currentStanding.watch_id,
      owner_kind: "person",
      project_id: PROJ_ID,
      revision: 99,
    });
    await waitFor(() => {
      expect(projectWatchesFor(PROJ_ID).standingWatches.length).toBe(0);
      expect(
        screen.queryByTestId("project-context-attention-running"),
      ).toBeNull();
      expect(
        screen.getByTestId("chat-header-standing-watch-deleted").textContent,
      ).toBe("Deleted standing watch · Nightly suite");
    });
    expect(
      noticeStore
        .index()
        .get(scopeKey)
        ?.some((n) => n.code === "standing_watch_paused") ?? false,
    ).toBe(false);

    openRunningStage({ projectId: PROJ_ID, createNew: true });
    expect(screen.getByTestId("standing-watch-editor")).toBeTruthy();

    stopNotices();
  });

  it("covers condition field editors, disabled notices, watch actions, and live section rows", async () => {
    const [conds, setConds] = createSignal<WatchCondition[]>([
      { kind: "timer", every_minutes: 60 },
    ]);
    render(() => (
      <StandingWatchConditionFields
        conditions={conds()}
        highlighted={true}
        onChange={setConds}
      />
    ));

    fireEvent.click(screen.getByTestId("standing-watch-add-condition"));
    expect(conds().length).toBe(2);

    // Switch condition 1 timer mode -> calendar -> edit days + times -> at -> edit at -> interval.
    fireEvent.click(screen.getByLabelText("Condition 1 timer mode"));
    fireEvent.click(screen.getByRole("option", { name: "Calendar" }));
    fireEvent.click(screen.getByLabelText("Condition 1 calendar days"));
    fireEvent.click(screen.getByRole("option", { name: "Every day" }));
    fireEvent.input(screen.getByLabelText("Condition 1 calendar times"), {
      target: { value: "08:30, 17:30" },
    });
    expect(conds()[0]?.calendar).toEqual({
      days: ["every_day"],
      times: ["08:30", "17:30"],
    });

    fireEvent.click(screen.getByLabelText("Condition 1 timer mode"));
    fireEvent.click(screen.getByRole("option", { name: "One-time date" }));
    fireEvent.input(screen.getByLabelText("Condition 1 at time"), {
      target: { value: "2027-06-01T10:00" },
    });
    expect(conds()[0]?.at).toBe("2027-06-01T10:00");

    // Switch condition 1 kind -> git_ref and edit ref + root; then file_change and edit path + glob.
    fireEvent.click(screen.getByLabelText("Condition 1 kind"));
    fireEvent.click(screen.getByRole("option", { name: "Git ref" }));
    fireEvent.input(screen.getByLabelText("Condition 1 git ref"), {
      target: { value: "refs/heads/release" },
    });
    fireEvent.input(screen.getByLabelText("Condition 1 root"), {
      target: { value: "repo" },
    });
    expect(conds()[0]).toEqual({
      kind: "git_ref",
      ref: "refs/heads/release",
      root_id: "repo",
    });

    fireEvent.click(screen.getByLabelText("Condition 1 kind"));
    fireEvent.click(screen.getByRole("option", { name: "File change" }));
    fireEvent.input(screen.getByLabelText("Condition 1 file path"), {
      target: { value: "packages/ui" },
    });
    fireEvent.input(screen.getByLabelText("Condition 1 glob"), {
      target: { value: "**/*.tsx" },
    });
    expect(conds()[0]).toEqual({
      kind: "file_change",
      path: "packages/ui",
      glob: "**/*.tsx",
    });

    fireEvent.click(screen.getAllByText("Remove")[0]!);
    expect(conds().length).toBe(1);

    // Exercise watch actions + disabled standing watch notice and action callback.
    const openedTargets: unknown[] = [];
    const unregisterSink = registerOpenRunningStageSink((target) => {
      openedTargets.push(target);
    });
    const noticeStore = createNoticeStore();
    const disabledSW = makeStandingWatch({
      state: "disabled",
      state_reason: "owner_access_revoked",
      revision: 3,
    });
    upsertStandingWatchInStore(disabledSW);

    const actionClient = stubClient({
      listProjectWatches: vi.fn(async () => ({
        watches: [
          {
            watch_id: disabledSW.watch_id,
            owner_kind: "person" as const,
            project_id: PROJ_ID,
            revision: disabledSW.revision,
            standing_watch: disabledSW,
          },
        ],
        daily_spend_cap_nano_usd: 0,
        has_unpriced_standing_watch: false,
        enabled_standing_total: 0,
      })),
      updateWatchDelivery: vi.fn(async (_sid, handle, req) =>
        makeAgentWatch({ handle, delivery: req.delivery, revision: 2 }),
      ),
      updateStandingWatch: vi.fn(async () => ({
        ...disabledSW,
        state: "enabled" as const,
        revision: 4,
      })),
      resumeStandingWatch: vi.fn(async () => ({
        ...disabledSW,
        state: "enabled" as const,
        revision: 5,
      })),
      runStandingWatchNow: vi.fn(async () => ({
        run_id: "00000000-0000-4000-8000-000000000401",
        watch_id: disabledSW.watch_id,
        watch_name: disabledSW.name,
        project_id: PROJ_ID,
        status: "running" as const,
        scheduled_at: "2026-04-20T12:00:00Z",
        missed_count: 0,
        spend_limit_nano_usd: 2_000_000_000,
        allow_unpriced: false,
        created_at: "2026-04-20T12:00:00Z",
      })),
      deleteStandingWatch: vi.fn(async () => undefined),
    });

    const stopDisabledNotices = mountStandingWatchNotices({
      notices: noticeStore,
      activeProjectId: () => PROJ_ID,
      getClient: () => actionClient,
    });

    const scopeKey = noticeScopeKey(projectScope(PROJ_ID));
    await waitFor(() => {
      const disabledNotice = noticeStore
        .index()
        .get(scopeKey)
        ?.find((n) => n.code === "standing_watch_disabled");
      expect(disabledNotice).toBeTruthy();
      disabledNotice?.interaction?.actions?.[0]?.run?.();
    });
    expect(openedTargets.length).toBe(1);

    await setSessionWatchDeliveryAction(actionClient, SESS_ID, "watch-9", "wake");
    await updateProjectStandingWatchAction(
      actionClient,
      PROJ_ID,
      disabledSW.watch_id,
      { revision: 3, state: "enabled" },
    );
    await resumeProjectStandingWatchAction(
      actionClient,
      PROJ_ID,
      disabledSW.watch_id,
      4,
    );
    await runProjectStandingWatchNowAction(
      actionClient,
      PROJ_ID,
      disabledSW.watch_id,
    );
    await deleteProjectStandingWatchAction(
      actionClient,
      PROJ_ID,
      disabledSW.watch_id,
    );

    stopDisabledNotices();
    unregisterSink();

    // Exercise ActivityLiveSection directly with background processes, derived watches, active runs, and actions.
    const liveStanding = makeStandingWatch({ name: "Deploy check", revision: 2 });
    const activeRun: WatchRun = {
      run_id: "00000000-0000-4000-8000-000000000501",
      watch_id: liveStanding.watch_id,
      watch_name: liveStanding.name,
      project_id: PROJ_ID,
      session_id: "sess-run",
      status: "running",
      scheduled_at: "2026-04-20T12:00:00Z",
      missed_count: 0,
      spend_limit_nano_usd: 2_000_000_000,
      allow_unpriced: false,
      created_at: "2026-04-20T12:00:00Z",
    };
    const derivedWatch = makeAgentWatch({
      handle: "watch-derived",
      derived: true,
      keepable: false,
      source_process_id: "proc-bg-1",
    });
    const plainKeepable = makeAgentWatch({
      handle: "watch-keepable",
      keepable: true,
    });
    const proposalReplace = makeAgentWatch({
      handle: "watch-prop-live",
      standing_draft: {
        name: "Deploy check",
        prompt: "Updated deploy check",
        replaces: liveStanding.watch_id,
      },
    });
    const proposalDismiss = makeAgentWatch({
      handle: "watch-prop-dismiss",
      standing_draft: {
        name: "Dismissable check",
        prompt: "Check later",
      },
    });

    const liveCancelWatch = vi.fn(async (_sid: string, handle: string) => {
      if (handle === "watch-keepable") {
        throw new Error("cancel failed");
      }
      return {
        watch: { ...proposalDismiss, handle, state: "terminal" as const },
        receipt: {
          receipt_id: "rec-dismiss",
          handle,
          terminal_outcome: "dismissed" as const,
          recorded_at: "2026-04-20T12:01:00Z",
        },
      };
    });
    const liveKeepWatch = vi.fn(async () => ({
      standing_watch: liveStanding,
      receipt: {
        receipt_id: "rec-kept",
        handle: "watch-prop-live",
        terminal_outcome: "kept" as const,
        standing_watch_id: liveStanding.watch_id,
        recorded_at: "2026-04-20T12:01:00Z",
      },
    }));

    const liveClient = stubClient({
      cancelWatch: liveCancelWatch,
      keepWatch: liveKeepWatch,
      listWatches: vi.fn(async () => ({ watches: [] })),
    });
    const onStopProc = vi.fn();
    const onOpenChat = vi.fn();
    const onSelectSW = vi.fn();
    const onPrefill = vi.fn();

    render(() => (
      <ActivityLiveSection
        runningGroups={[
          {
            sessionId: SESS_ID,
            sessionTitle: "Build session",
            sessionArchived: true,
            processes: [
              {
                process_id: "proc-bg-1",
                project_id: PROJ_ID,
                session_id: SESS_ID,
                session_archived: true,
                running: true,
                command: "npm run dev",
                working_dir: "/workspace/app",
                ports: ["3000", "3001"],
                started_at: "2026-04-20T12:00:00Z",
              },
            ],
          },
        ]}
        liveAgentWatches={[
          derivedWatch,
          plainKeepable,
          proposalReplace,
          proposalDismiss,
        ]}
        activeRunBySession={
          new Map([["sess-run", { watch: liveStanding, run: activeRun }]])
        }
        standingById={new Map([[liveStanding.watch_id, liveStanding]])}
        queuedRuns={[]}
        highlightedSessionId={SESS_ID}
        isDisconnected={false}
        isStopping={() => false}
        stopError={(pid) => (pid === "proc-bg-1" ? "SIGTERM timed out" : null)}
        client={() => liveClient}
        onStopProcess={onStopProc}
        onOpenSession={onOpenChat}
        onSelectStandingWatch={onSelectSW}
        onPrefillFromAgentWatch={onPrefill}
      />
    ));

    expect(screen.getByTestId("project-running-ports-proc-bg-1").textContent).toBe(
      "Ports 3000, 3001",
    );
    expect(
      screen.getByTestId("project-running-stop-error-proc-bg-1").textContent,
    ).toBe("SIGTERM timed out");

    fireEvent.click(screen.getByTestId("project-running-open-chat-proc-bg-1"));
    expect(onOpenChat).toHaveBeenCalledWith(SESS_ID);
    fireEvent.click(screen.getByTestId("project-running-stop-proc-bg-1"));
    expect(onStopProc).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByTestId("activity-live-run-watch-sess-run"));
    expect(onSelectSW).toHaveBeenCalledWith(liveStanding.watch_id);

    fireEvent.click(screen.getByTestId("activity-watch-keep-watch-keepable"));
    expect(onPrefill).toHaveBeenCalledWith(plainKeepable);

    fireEvent.click(screen.getByTestId("activity-watch-cancel-watch-keepable"));
    await waitFor(() => {
      expect(
        screen.getByTestId("activity-watch-error-watch-keepable").textContent,
      ).toBe("cancel failed");
    });

    fireEvent.click(screen.getByTestId("activity-proposal-edit-watch-prop-live"));
    expect(onPrefill).toHaveBeenCalledWith(proposalReplace);

    fireEvent.click(screen.getByTestId("activity-proposal-keep-watch-prop-live"));
    await waitFor(() => {
      expect(liveKeepWatch).toHaveBeenCalledWith(
        SESS_ID,
        "watch-prop-live",
        { revision: 2 },
      );
    });

    fireEvent.click(
      screen.getByTestId("activity-proposal-dismiss-watch-prop-dismiss"),
    );
    await waitFor(() => {
      expect(liveCancelWatch).toHaveBeenCalledWith(SESS_ID, "watch-prop-dismiss");
    });
  });
});
