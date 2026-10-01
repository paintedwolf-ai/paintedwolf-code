import { describe, expect, it } from "vitest";
import type { DenLayoutPrefs } from "../../shared/app-state-types.ts";
import {
  CHAT_COL_MIN,
  CHAT_WIDTH_DEFAULT_PX,
  STAGE_COL_MIN,
  clampChatWidthPx,
  DIVIDER_PX,
  maxChatWidthPx,
  narrowSurvivorFollowsLaunch,
  resolveChatWidthPx,
  resolveMode,
  resolveNarrowSurvivor,
  resolveSplitCompanion,
  resolveStartupCompanion,
  resolveStoredCompanion,
  resolveWorkspaceOrientation,
  snapChatWidthPx,
  SPLIT_MIN_HOST,
  splitColumnsOnScreen,
  stageShare,
  startupPlacementDecision,
  withChatWidthPx,
  withCompanion,
  withNarrowSurvivor,
  withPlacement,
  withStartupCompanion,
} from "./stage-placement.ts";

describe("stage-placement resolver", () => {
  it("returns the stored mode, defaulting to inline", () => {
    expect(resolveMode({ mode: "split" })).toBe("split");
    expect(resolveMode(undefined)).toBe("inline");
    expect(resolveMode({})).toBe("inline");
    expect(
      resolveMode({ mode: "nope" as DenLayoutPrefs["mode"] }),
    ).toBe("inline");
  });

  it("resolves the launch companion, rejecting unknown stages", () => {
    expect(resolveStartupCompanion({ startupCompanion: "files" })).toBe("files");
    expect(resolveStartupCompanion(undefined)).toBeNull();
    expect(resolveStartupCompanion({})).toBeNull();
    expect(
      resolveStartupCompanion({
        startupCompanion: "nope" as DenLayoutPrefs["startupCompanion"],
      }),
    ).toBeNull();
  });

  it("resolves a stored companion, rejecting unknown stages", () => {
    expect(resolveStoredCompanion({ companion: "search" })).toBe("search");
    expect(resolveStoredCompanion({ companion: "files" })).toBe("files");
    expect(resolveStoredCompanion(undefined)).toBeNull();
    expect(resolveStoredCompanion({})).toBeNull();
    expect(
      resolveStoredCompanion({
        companion: "nope" as DenLayoutPrefs["companion"],
      }),
    ).toBeNull();
  });

  it("defaults a live split companion to Files", () => {
    expect(resolveSplitCompanion(null)).toBe("files");
    expect(resolveSplitCompanion("search")).toBe("search");
    expect(
      resolveSplitCompanion("security", (id) => id !== "security"),
    ).toBe("files");
  });

  it("writes the companion with split and keeps it when returning to inline", () => {
    const split = withPlacement(undefined, {
      mode: "split",
      companion: "search",
    });
    expect(split).toEqual({ mode: "split", companion: "search" });
    expect(withPlacement(split, { mode: "inline" })).toEqual({
      mode: "inline",
      companion: "search",
    });
  });

  it("writes and clears a remembered companion", () => {
    expect(withCompanion({ mode: "inline" }, "files")).toEqual({
      mode: "inline",
      companion: "files",
    });
    expect(withCompanion({ companion: "search" }, null)).toEqual({});
  });

  it("writes and clears the launch companion", () => {
    const set = withStartupCompanion({ mode: "split" }, "files");
    expect(set).toEqual({ mode: "split", startupCompanion: "files" });
    expect(withStartupCompanion(set, null)).toEqual({ mode: "split" });
    expect(withStartupCompanion(undefined, "search")).toEqual({
      startupCompanion: "search",
    });
  });

  it("follows the launch layout for the column a narrow split keeps", () => {
    expect(resolveNarrowSurvivor(undefined)).toBe("conversation");
    expect(resolveNarrowSurvivor({})).toBe("conversation");
    expect(resolveNarrowSurvivor({ startupCompanion: "files" })).toBe("stage");
    expect(resolveNarrowSurvivor({ startupCompanion: "search" })).toBe("stage");
    // Non-stage companions contribute no minimum width.
    expect(
      resolveNarrowSurvivor({
        startupCompanion: "nope" as DenLayoutPrefs["startupCompanion"],
      }),
    ).toBe("conversation");
  });

  it("keeps a pinned survivor against the launch layout", () => {
    expect(
      resolveNarrowSurvivor({
        startupCompanion: "files",
        narrowSurvivor: "conversation",
      }),
    ).toBe("conversation");
    expect(resolveNarrowSurvivor({ narrowSurvivor: "stage" })).toBe("stage");
    expect(
      resolveNarrowSurvivor({
        narrowSurvivor: "nope" as DenLayoutPrefs["narrowSurvivor"],
      }),
    ).toBe("conversation");
  });

  it("reports whether the survivor is pinned or derived", () => {
    expect(narrowSurvivorFollowsLaunch(undefined)).toBe(true);
    expect(narrowSurvivorFollowsLaunch({ startupCompanion: "files" })).toBe(true);
    expect(narrowSurvivorFollowsLaunch({ narrowSurvivor: "stage" })).toBe(false);
    expect(
      narrowSurvivorFollowsLaunch({ narrowSurvivor: "conversation" }),
    ).toBe(false);
  });

  it("writes and clears the pinned survivor", () => {
    const pinned = withNarrowSurvivor({ mode: "split" }, "stage");
    expect(pinned).toEqual({ mode: "split", narrowSurvivor: "stage" });
    expect(withNarrowSurvivor(pinned, null)).toEqual({ mode: "split" });
    expect(withNarrowSurvivor(undefined, "conversation")).toEqual({
      narrowSurvivor: "conversation",
    });
  });

  it("calls a column the band dropped off screen, not merely hidden", () => {
    // Seen stamps and the workspace registry ride on this: a turn finishing
    // in a column the width took away still deserves a dot.
    const at = (over: Partial<Parameters<typeof splitColumnsOnScreen>[0]>) =>
      splitColumnsOnScreen({
        splitLive: true,
        hidden: null,
        hostWidthPx: SPLIT_MIN_HOST,
        narrowSurvivor: "stage",
        ...over,
      });
    const both = { conversation: true, stage: true };
    expect(at({})).toEqual(both);
    expect(at({ hostWidthPx: SPLIT_MIN_HOST - 1 })).toEqual({
      conversation: false,
      stage: true,
    });
    expect(
      at({ hostWidthPx: SPLIT_MIN_HOST - 1, narrowSurvivor: "conversation" }),
    ).toEqual({ conversation: true, stage: false });
    // A committed hide answers before any width does and leaves the stage alone.
    const hidden = { conversation: false, stage: true };
    expect(at({ hidden: "conversation" })).toEqual(hidden);
    expect(
      at({
        hidden: "conversation",
        hostWidthPx: SPLIT_MIN_HOST - 1,
        narrowSurvivor: "conversation",
      }),
    ).toEqual(hidden);
    expect(at({ hidden: "stage", hostWidthPx: 500 })).toEqual({ conversation: true, stage: false });
    // Single-column mode ignores the split-width threshold.
    expect(at({ splitLive: false, hostWidthPx: 0 })).toEqual(both);
  });

  it("holds the launch layout until a conversation is live", () => {
    const available = () => true;
    expect(
      startupPlacementDecision({
        firstRunPending: false,
        chatLive: false,
        launchCompanion: "files",
        splitMode: false,
        persistedCompanion: null,
        isAvailable: available,
      }),
    ).toEqual({ settled: false, openStage: null });

    expect(
      startupPlacementDecision({
        firstRunPending: false,
        chatLive: true,
        launchCompanion: "files",
        splitMode: false,
        persistedCompanion: null,
        isAvailable: available,
      }),
    ).toEqual({ settled: true, openStage: "files" });
  });

  // A starting layout chosen during first run must still be unspent when the
  // workspace appears, even if a restored chat was live behind the gate.
  it("holds the launch layout while first run is on screen", () => {
    expect(
      startupPlacementDecision({
        firstRunPending: true,
        chatLive: true,
        launchCompanion: "files",
        splitMode: false,
        persistedCompanion: null,
        isAvailable: () => true,
      }),
    ).toEqual({ settled: false, openStage: null });
  });

  it("spends the launch layout once, with or without a split to open", () => {
    const available = () => true;
    // Inline, no launch companion.
    expect(
      startupPlacementDecision({
        firstRunPending: false,
        chatLive: true,
        launchCompanion: null,
        splitMode: false,
        persistedCompanion: null,
        isAvailable: available,
      }),
    ).toEqual({ settled: true, openStage: null });

    // Stored split, no companion.
    expect(
      startupPlacementDecision({
        firstRunPending: false,
        chatLive: true,
        launchCompanion: null,
        splitMode: true,
        persistedCompanion: null,
        isAvailable: available,
      }),
    ).toEqual({ settled: true, openStage: "files" });

    // Launch companion is unavailable.
    expect(
      startupPlacementDecision({
        firstRunPending: false,
        chatLive: true,
        launchCompanion: "security",
        splitMode: false,
        persistedCompanion: null,
        isAvailable: (id) => id !== "security",
      }),
    ).toEqual({ settled: true, openStage: "files" });
  });

  it("stores one placement for the shell", () => {
    const prefs: DenLayoutPrefs = { mode: "split" };
    expect(resolveMode(prefs)).toBe("split");
    expect(resolveMode(prefs)).toBe(resolveMode({ ...prefs }));
  });
});

describe("split host floor", () => {
  it("is the sum of the two column floors and the seam", () => {
    expect(SPLIT_MIN_HOST).toBe(STAGE_COL_MIN + CHAT_COL_MIN + DIVIDER_PX);
  });

  it("holds the stage at its floor for every width a split is live at", () => {
    // The narrowest live split still gives the stage its whole floor, so a
    // stage never loses anything it would get back when the split collapses.
    const stageAt = (host: number) =>
      host - DIVIDER_PX - clampChatWidthPx(CHAT_WIDTH_DEFAULT_PX, host);
    expect(stageAt(SPLIT_MIN_HOST)).toBe(STAGE_COL_MIN);
    expect(stageAt(SPLIT_MIN_HOST + 400)).toBeGreaterThan(STAGE_COL_MIN);
  });
});

describe("workspace orientation", () => {
  it("defaults to standard and accepts mirrored", () => {
    expect(resolveWorkspaceOrientation(undefined)).toBe("standard");
    expect(resolveWorkspaceOrientation({})).toBe("standard");
    expect(resolveWorkspaceOrientation({ workspaceOrientation: "mirrored" })).toBe(
      "mirrored",
    );
  });
});

describe("the conversation keeps its width", () => {
  it("does not depend on orientation or companion", () => {
    const prefs: DenLayoutPrefs = {
      workspaceOrientation: "mirrored",
      chatWidthPx: 480,
    };
    expect(resolveChatWidthPx(prefs)).toBe(480);
    expect(
      resolveChatWidthPx({
        ...prefs,
        workspaceOrientation: "standard",
        companion: "search",
      }),
    ).toBe(480);
  });

  it("gives every window resize to the stage", () => {
    const at = (host: number) => {
      const chat = clampChatWidthPx(480, host);
      return { chat, stage: host - DIVIDER_PX - chat };
    };
    expect(at(1400)).toEqual({ chat: 480, stage: 919 });
    expect(at(1800)).toEqual({ chat: 480, stage: 1319 });
  });

  it("reports the stage share for a conversation width", () => {
    expect(stageShare(400, 1201)).toBeCloseTo(2 / 3);
    expect(stageShare(400, 0)).toBe(0);
  });
});

describe("clampChatWidthPx", () => {
  it("narrows the conversation only once the stage reaches its floor", () => {
    const host = STAGE_COL_MIN + 480 + DIVIDER_PX;
    expect(clampChatWidthPx(480, host + 200)).toBe(480);
    expect(clampChatWidthPx(480, host)).toBe(480);
    expect(clampChatWidthPx(480, host - 40)).toBe(440);
  });

  it("never narrows the conversation below its floor", () => {
    expect(clampChatWidthPx(100, 1600)).toBe(CHAT_COL_MIN);
  });

  it("keeps the preference when the split cannot fit at all", () => {
    expect(clampChatWidthPx(480, SPLIT_MIN_HOST - 1)).toBe(480);
    expect(clampChatWidthPx(480, 0)).toBe(480);
  });
});

describe("maxChatWidthPx", () => {
  it("leaves the stage its floor — the same floor a drag stops at", () => {
    expect(maxChatWidthPx(1400)).toBe(1400 - DIVIDER_PX - STAGE_COL_MIN);
    expect(maxChatWidthPx(SPLIT_MIN_HOST)).toBe(CHAT_COL_MIN);
  });

  it("is unbounded where the split cannot be laid out at all", () => {
    expect(maxChatWidthPx(SPLIT_MIN_HOST - 1)).toBe(Number.POSITIVE_INFINITY);
    expect(maxChatWidthPx(Number.NaN)).toBe(Number.POSITIVE_INFINITY);
  });
});

describe("snapChatWidthPx", () => {
  it("snaps the stage to a half or third within tolerance", () => {
    const host = 1201;
    expect(snapChatWidthPx(610, host)).toBe(600);
    expect(snapChatWidthPx(790, host)).toBe(800);
    expect(snapChatWidthPx(480, host)).toBe(480);
  });
});

describe("withChatWidthPx", () => {
  it("writes one conversation width and leaves the mode alone", () => {
    const prefs = withChatWidthPx({ mode: "split" }, 480.4);
    expect(prefs).toEqual({ mode: "split", chatWidthPx: 480 });
    expect(withChatWidthPx(prefs, 200)).toEqual({
      mode: "split",
      chatWidthPx: CHAT_COL_MIN,
    });
  });
});

describe("defaults", () => {
  it("opens a split at the default conversation width", () => {
    expect(resolveChatWidthPx(undefined)).toBe(CHAT_WIDTH_DEFAULT_PX);
    expect(resolveChatWidthPx({ chatWidthPx: Number.NaN })).toBe(
      CHAT_WIDTH_DEFAULT_PX,
    );
  });

  it("reserves only the visual seam between split panes", () => {
    expect(DIVIDER_PX).toBe(1);
  });
});
