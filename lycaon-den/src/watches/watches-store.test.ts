// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../shared/app-state-types.ts";
import type { AgentWatch } from "../api/types.ts";
import { loadAppState } from "../platform/persistence/app-state.ts";
import { parseAppState } from "../platform/persistence/app-state-parse.ts";
import {
  applyBootAppStateSnapshot,
  applyDevicePrefsFromSnapshot,
} from "../platform/persistence/app-state-sync.ts";
import {
  appStateUnwritten,
  flushAppState,
  getAppStateSnapshot,
  loadSharedAppState,
  persistAppState,
  resetAppStateSnapshotForTests,
  setAppStateSnapshot,
} from "../store/app-state-snapshot.ts";
import {
  hideProposalOverflow,
  isProposalOverflowHidden,
  retainProjectWatchesProject,
  syncHiddenProposalOverflowsFromSnapshot,
} from "./watches-store.ts";

const PROJ_ID = "00000000-0000-4000-8000-000000000001";
const SESS_ID = "00000000-0000-4000-8000-000000000002";

function makeProposal(index: number): AgentWatch {
  return {
    watch_id: `00000000-0000-4000-8000-00000000010${index}`,
    handle: `watch-${index}`,
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
    created_at: `2026-04-20T10:0${index}:00Z`,
    updated_at: `2026-04-20T10:0${index}:00Z`,
    standing_draft: {
      name: `Proposal ${index}`,
      prompt: `Check ${index}`,
    },
  };
}

describe("watches-store hiddenProposalOverflows persistence and hydration", () => {
  beforeEach(() => {
    localStorage.clear();
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    retainProjectWatchesProject(null);
  });

  it("persists hiddenProposalOverflows, hydrates after loadSharedAppState + boot sync, and reappears on watermark change", async () => {
    const fiveProposals = [1, 2, 3, 4, 5].map(makeProposal);

    expect(isProposalOverflowHidden(SESS_ID, fiveProposals)).toBe(false);

    hideProposalOverflow(SESS_ID, fiveProposals);
    expect(isProposalOverflowHidden(SESS_ID, fiveProposals)).toBe(true);
    expect(getAppStateSnapshot().hiddenProposalOverflows).toEqual({
      [SESS_ID]: "watch-3:watch-5",
    });

    // Wait for the immediate flush to settle and verify disk/storage persistence.
    await flushAppState();
    const onDisk = await loadAppState();
    expect(onDisk.hiddenProposalOverflows).toEqual({
      [SESS_ID]: "watch-3:watch-5",
    });

    // Simulate reload: snapshot and watches-store signal start empty after module import,
    // then async loadSharedAppState() + applyBootAppStateSnapshot() run.
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    retainProjectWatchesProject(null);
    expect(isProposalOverflowHidden(SESS_ID, fiveProposals)).toBe(false);

    await loadSharedAppState();
    applyBootAppStateSnapshot();

    // Same 5 proposals remain hidden after reload hydration.
    expect(isProposalOverflowHidden(SESS_ID, fiveProposals)).toBe(true);

    // Watermark change (a 6th proposal arrives) causes overflow to reappear live.
    const sixProposals = [...fiveProposals, makeProposal(6)];
    expect(isProposalOverflowHidden(SESS_ID, sixProposals)).toBe(false);

    // Watermark change (oldest hidden proposal changes) also causes overflow to reappear.
    const shiftedProposals = [1, 2, 4, 5].map(makeProposal);
    expect(isProposalOverflowHidden(SESS_ID, shiftedProposals)).toBe(false);
  });

  it("leaves a local hide in effect across an unrelated peer app-state apply", async () => {
    const fiveProposals = [1, 2, 3, 4, 5].map(makeProposal);

    hideProposalOverflow(SESS_ID, fiveProposals);
    await flushAppState();
    expect(isProposalOverflowHidden(SESS_ID, fiveProposals)).toBe(true);

    // Simulate an unrelated peer-window preference patch arriving and applying device prefs.
    setAppStateSnapshot(
      parseAppState({
        ...getAppStateSnapshot(),
        display: { diffWordWrap: true },
      }),
    );
    applyDevicePrefsFromSnapshot();

    expect(getAppStateSnapshot().hiddenProposalOverflows).toEqual({
      [SESS_ID]: "watch-3:watch-5",
    });
    expect(isProposalOverflowHidden(SESS_ID, fiveProposals)).toBe(true);
  });

  it("does not roll back a pending local hide when snapshot sync runs while unwritten", async () => {
    const fiveProposals = [1, 2, 3, 4, 5].map(makeProposal);

    hideProposalOverflow(SESS_ID, fiveProposals);
    await flushAppState();

    // Queue a pending/unwritten hiddenProposalOverflows write and simulate an older snapshot.
    const pending = persistAppState({
      hiddenProposalOverflows: { [SESS_ID]: "watch-3:watch-5" },
    });
    expect(appStateUnwritten("hiddenProposalOverflows")).toBe(true);

    setAppStateSnapshot(
      parseAppState({
        ...EMPTY_APP_STATE_V1,
        hiddenProposalOverflows: { "other-sess": "watch-3:watch-4" },
      }),
    );
    syncHiddenProposalOverflowsFromSnapshot();

    expect(isProposalOverflowHidden(SESS_ID, fiveProposals)).toBe(true);
    await flushAppState();
    await pending;
  });
});
