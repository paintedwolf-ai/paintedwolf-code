import { afterEach, describe, expect, it } from "vitest";

import {
  chatTabRailBindings,
  setChatTabRailBindings,
  type ChatTabRailBindings,
} from "./chat-tab-rail-bindings.ts";

function makeBindings(): ChatTabRailBindings {
  return {
    workflows: {
      workflowPickerOpen: () => false,
      workflowError: () => null,
      workflowBusy: () => false,
      onExit: () => {},
      onPause: () => {},
      onResume: () => {},
      onAdvance: () => {},
      onReviewInChat: () => {},
      onOpenPicker: () => {},
      onClosePicker: () => {},
      onArmWorkflow: () => {},
      onJumpToRun: () => {},
    },
    git: {
      projectId: "p1",
      rootRefs: [],
      busy: () => false,
      onRefreshRepos: () => {},
      onScopePin: () => {},
      onSelectRepo: () => {},
      onInit: () => {},
      onCommit: async () => true,
      onStash: () => {},
      onDraftMessage: async () => "",
      onListBranches: async () => [],
      onCheckout: async () => {},
      onDiscard: () => {},
      onPush: () => {},
      onPull: () => {},
      onGroupCommit: () => {},
      onRefreshWorktree: async () => undefined,
      onBindWorktree: async () => ({
        bound: false,
        session_id: "s1",
        dirty: false,
        ahead_of_base: 0,
        behind_base: 0,
      }),
      onLandWorktree: async () => ({
        landed: false,
        base_branch: "main",
        commits: 0,
        reason: "nothing_to_land",
        conflicts: [],
      }),
      onUnbindWorktree: async () => {},
    },
    onOpenWorklog: () => {},
    onCloseWorklog: () => {},
    worklogOpen: () => false,
    onCancelWorker: () => {},
    cancellingWorkerId: () => null,
  };
}

afterEach(() => {
  // Force-clear the module global regardless of which token holds the claim.
  const sweeper = {};
  setChatTabRailBindings(makeBindings(), sweeper);
  setChatTabRailBindings(null, sweeper);
});

describe("setChatTabRailBindings claims", () => {
  it("sets and clears bindings for a single claim token", () => {
    const claimToken = {};
    const bindings = makeBindings();
    setChatTabRailBindings(bindings, claimToken);
    expect(chatTabRailBindings()).toBe(bindings);
    setChatTabRailBindings(null, claimToken);
    expect(chatTabRailBindings()).toBeNull();
  });

  it("resident chat layers: idle ChatView's release does not clobber the active ChatView's bindings", () => {
    // A hidden resident chat can release after another chat has claimed the rail.
    const outgoing = {};
    const incoming = {};
    const fresh = makeBindings();

    setChatTabRailBindings(makeBindings(), outgoing);
    setChatTabRailBindings(fresh, incoming);
    setChatTabRailBindings(null, outgoing);

    expect(chatTabRailBindings()).toBe(fresh);
  });

  it("returning to an idle chat reclaims the rail", () => {
    const first = {};
    const second = {};
    const firstBindings = makeBindings();
    const secondBindings = makeBindings();

    setChatTabRailBindings(firstBindings, first);
    setChatTabRailBindings(secondBindings, second);
    setChatTabRailBindings(null, first);
    expect(chatTabRailBindings()).toBe(secondBindings);
    setChatTabRailBindings(firstBindings, first);
    expect(chatTabRailBindings()).toBe(firstBindings);
  });

  it("the current claimant can release after superseding a previous claimant", () => {
    const outgoing = {};
    const incoming = {};
    setChatTabRailBindings(makeBindings(), outgoing);
    setChatTabRailBindings(makeBindings(), incoming);
    setChatTabRailBindings(null, outgoing);
    setChatTabRailBindings(null, incoming);
    expect(chatTabRailBindings()).toBeNull();
  });
});
