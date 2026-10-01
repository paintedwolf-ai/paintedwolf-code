import { describe, expect, it } from "vitest";
import {
  APP_NAV_STAGE_MIN_PX,
  AUTO_COLLAPSE_RESTORE_HEADROOM_PX,
  filesShowWindowDeficitPx,
  shouldAutomaticallyCollapseNav,
  stageOpenWindowDeficitPx,
  workspaceStageFloorPx,
} from "./responsive-collapse.ts";
import { CHAT_COL_MIN, DIVIDER_PX, STAGE_COL_MIN } from "./stage-placement.ts";

describe("responsive sidebar collapse", () => {
  const base = {
    automaticCollapsed: false,
    viewportWidthPx: 1200,
    navWidthPx: 280,
    stageMinWidthPx: APP_NAV_STAGE_MIN_PX,
    splitColumns: false,
    chatWidthPx: CHAT_COL_MIN,
  };

  it("counts nav width when showing a person-hidden rail", () => {
    expect(
      stageOpenWindowDeficitPx({
        viewportWidthPx: 900,
        navWidthPx: base.navWidthPx,
        countNavWidth: true,
        stageMinWidthPx: APP_NAV_STAGE_MIN_PX,
        splitColumns: false,
        chatWidthPx: CHAT_COL_MIN,
      }),
    ).toBeGreaterThan(0);
  });

  it("yields the application sidebar before an inline stage is crowded", () => {
    const threshold = base.navWidthPx + APP_NAV_STAGE_MIN_PX;
    expect(
      shouldAutomaticallyCollapseNav({
        ...base,
        viewportWidthPx: threshold - 1,
      }),
    ).toBe(true);
    expect(
      shouldAutomaticallyCollapseNav({ ...base, viewportWidthPx: threshold }),
    ).toBe(false);
  });

  it("yields the sidebar before the conversation narrows", () => {
    const chatWidthPx = 520;
    const threshold =
      base.navWidthPx + STAGE_COL_MIN + chatWidthPx + DIVIDER_PX;
    const split = {
      ...base,
      stageMinWidthPx: STAGE_COL_MIN,
      splitColumns: true,
      chatWidthPx,
    };
    expect(
      shouldAutomaticallyCollapseNav({ ...split, viewportWidthPx: threshold }),
    ).toBe(false);
    expect(
      shouldAutomaticallyCollapseNav({ ...split, viewportWidthPx: threshold - 1 }),
    ).toBe(true);
  });

  it("stops reserving the conversation column once the conversation is hidden", () => {
    const stageOnly = base.navWidthPx + STAGE_COL_MIN;
    const hidden = {
      ...base,
      viewportWidthPx: stageOnly,
      stageMinWidthPx: STAGE_COL_MIN,
    };
    expect(
      shouldAutomaticallyCollapseNav({ ...hidden, splitColumns: true }),
    ).toBe(true);
    expect(
      shouldAutomaticallyCollapseNav({ ...hidden, splitColumns: false }),
    ).toBe(false);
  });

  it("claims the conversation's own width when it is shown again", () => {
    const stageOnly = base.navWidthPx + STAGE_COL_MIN;
    expect(
      stageOpenWindowDeficitPx({
        viewportWidthPx: stageOnly,
        navWidthPx: base.navWidthPx,
        countNavWidth: true,
        stageMinWidthPx: STAGE_COL_MIN,
        splitColumns: true,
        chatWidthPx: 520,
      }),
    ).toBe(520 + DIVIDER_PX + AUTO_COLLAPSE_RESTORE_HEADROOM_PX);
  });

  it("restores automatically with a small hysteresis band", () => {
    const threshold = base.navWidthPx + APP_NAV_STAGE_MIN_PX;
    expect(
      shouldAutomaticallyCollapseNav({
        ...base,
        automaticCollapsed: true,
        viewportWidthPx: threshold + AUTO_COLLAPSE_RESTORE_HEADROOM_PX - 1,
      }),
    ).toBe(true);
    expect(
      shouldAutomaticallyCollapseNav({
        ...base,
        automaticCollapsed: true,
        viewportWidthPx: threshold + AUTO_COLLAPSE_RESTORE_HEADROOM_PX,
      }),
    ).toBe(false);
  });

  it("does not re-expand Files when the application sidebar yields", () => {
    const threshold = base.navWidthPx + STAGE_COL_MIN;
    const files = {
      ...base,
      stageMinWidthPx: STAGE_COL_MIN,
    };
    expect(
      shouldAutomaticallyCollapseNav({ ...files, viewportWidthPx: threshold }),
    ).toBe(false);
    expect(
      shouldAutomaticallyCollapseNav({
        ...files,
        viewportWidthPx: threshold - 1,
      }),
    ).toBe(true);
    expect(threshold - 1).toBeGreaterThan(STAGE_COL_MIN);
  });

  it("includes nav width when the person did not hide it", () => {
    expect(
      stageOpenWindowDeficitPx({
        viewportWidthPx: 1000,
        navWidthPx: 280,
        countNavWidth: true,
        stageMinWidthPx: APP_NAV_STAGE_MIN_PX,
        splitColumns: true,
        chatWidthPx: CHAT_COL_MIN,
      }),
    ).toBe(325);
  });

  it("claims an inline stage without restoring a person-hidden sidebar", () => {
    expect(
      stageOpenWindowDeficitPx({
        viewportWidthPx: 700,
        navWidthPx: 280,
        countNavWidth: false,
        stageMinWidthPx: APP_NAV_STAGE_MIN_PX,
        splitColumns: false,
        chatWidthPx: CHAT_COL_MIN,
      }),
    ).toBe(APP_NAV_STAGE_MIN_PX + AUTO_COLLAPSE_RESTORE_HEADROOM_PX - 700);
  });

  it("claims split chrome past the Files navigator seam", () => {
    expect(
      stageOpenWindowDeficitPx({
        viewportWidthPx: 1000,
        navWidthPx: 280,
        countNavWidth: true,
        stageMinWidthPx: STAGE_COL_MIN,
        splitColumns: true,
        chatWidthPx: CHAT_COL_MIN,
      }),
    ).toBe(381);
    expect(
      stageOpenWindowDeficitPx({
        viewportWidthPx: 1600,
        navWidthPx: 280,
        countNavWidth: true,
        stageMinWidthPx: STAGE_COL_MIN,
        splitColumns: false,
        chatWidthPx: CHAT_COL_MIN,
      }),
    ).toBe(0);
  });

  it("shows Files without also showing an auto-hidden app nav", () => {
    expect(
      filesShowWindowDeficitPx({
        viewportWidthPx: 1000,
        navWidthPx: 280,
        navCurrentlyVisible: false,
        navUserCollapsed: false,
        splitColumns: true,
        chatWidthPx: CHAT_COL_MIN,
      }),
    ).toBe(81);
  });

  it("shows Files beside the conversation at its own width", () => {
    expect(
      filesShowWindowDeficitPx({
        viewportWidthPx: 1000,
        navWidthPx: 280,
        navCurrentlyVisible: false,
        navUserCollapsed: false,
        splitColumns: true,
        chatWidthPx: 520,
      }),
    ).toBe(81 + 520 - CHAT_COL_MIN);
  });

  it("accounts for a visible app nav at the split minimum", () => {
    expect(
      filesShowWindowDeficitPx({
        viewportWidthPx: 1165,
        navWidthPx: 280,
        navCurrentlyVisible: true,
        navUserCollapsed: false,
        splitColumns: true,
        chatWidthPx: CHAT_COL_MIN,
      }),
    ).toBe(196);
  });

  it("uses the Files floor for every live-split companion", () => {
    expect(STAGE_COL_MIN).toBeGreaterThan(APP_NAV_STAGE_MIN_PX);
    expect(
      workspaceStageFloorPx({ splitLive: true, stageId: "files" }),
    ).toBe(STAGE_COL_MIN);
    expect(
      workspaceStageFloorPx({ splitLive: true, stageId: "search" }),
    ).toBe(STAGE_COL_MIN);
    expect(
      workspaceStageFloorPx({ splitLive: false, stageId: "search" }),
    ).toBe(APP_NAV_STAGE_MIN_PX);
    expect(
      workspaceStageFloorPx({ splitLive: false, stageId: "files" }),
    ).toBe(STAGE_COL_MIN);
  });
});
