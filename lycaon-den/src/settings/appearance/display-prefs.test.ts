// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import {
  diffCollapsedPref,
  diffSplitPref,
  diffWordWrapPref,
  resolveDiffCollapsed,
  resolveDiffSplit,
  resolveDiffWordWrap,
  resolveShowAllModels,
  saveDiffCollapsed,
  saveDiffSplit,
  saveDiffWordWrap,
  saveShowAllModels,
  showAllModelsPref,
  syncDisplayPrefsFromSnapshot,
} from "./display-prefs.ts";
import { loadAppState } from "../../platform/persistence/app-state.ts";
import {
  getAppStateSnapshot,
  setAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";

describe("display-prefs", () => {
  beforeEach(() => {
    localStorage.clear();
    setAppStateSnapshot({ ...EMPTY_APP_STATE_V1 });
    syncDisplayPrefsFromSnapshot();
  });

  it("defaults diff word wrap to false", () => {
    expect(resolveDiffWordWrap()).toBe(false);
    expect(diffWordWrapPref()).toBe(false);
  });

  it("persists diff word wrap toggle", async () => {
    await saveDiffWordWrap(true);
    expect(diffWordWrapPref()).toBe(true);
    expect(getAppStateSnapshot().display?.diffWordWrap).toBe(true);
    const loaded = await loadAppState();
    expect(loaded.display?.diffWordWrap).toBe(true);
  });

  it("defaults diff collapsed to true", () => {
    expect(resolveDiffCollapsed()).toBe(true);
    expect(diffCollapsedPref()).toBe(true);
  });

  it("persists diff collapsed toggle without clobbering word wrap", async () => {
    await saveDiffWordWrap(true);
    await saveDiffCollapsed(false);
    expect(diffCollapsedPref()).toBe(false);
    expect(getAppStateSnapshot().display?.diffCollapsed).toBe(false);
    expect(getAppStateSnapshot().display?.diffWordWrap).toBe(true);
    const loaded = await loadAppState();
    expect(loaded.display?.diffCollapsed).toBe(false);
    expect(loaded.display?.diffWordWrap).toBe(true);
  });

  it("defaults diff split to false", () => {
    expect(resolveDiffSplit()).toBe(false);
    expect(diffSplitPref()).toBe(false);
  });

  it("persists diff split through save and disk", async () => {
    await saveDiffSplit(true);
    expect(diffSplitPref()).toBe(true);
    expect(getAppStateSnapshot().display?.diffSplit).toBe(true);
    const loaded = await loadAppState();
    expect(loaded.display?.diffSplit).toBe(true);
  });

  it("defaults show all models to false", () => {
    expect(resolveShowAllModels()).toBe(false);
    expect(showAllModelsPref()).toBe(false);
  });

  it("persists show all models without clobbering diff prefs", async () => {
    await saveDiffWordWrap(false);
    await saveShowAllModels(true);
    expect(showAllModelsPref()).toBe(true);
    expect(getAppStateSnapshot().display?.showAllModels).toBe(true);
    expect(getAppStateSnapshot().display?.diffWordWrap).toBe(false);
    const loaded = await loadAppState();
    expect(loaded.display?.showAllModels).toBe(true);
    expect(loaded.display?.diffWordWrap).toBe(false);
  });
});
