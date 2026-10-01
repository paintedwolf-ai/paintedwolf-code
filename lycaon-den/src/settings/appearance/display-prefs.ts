import { createSignal } from "solid-js";
import type { DenDisplayPrefs } from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";

const DEFAULT_DIFF_WORD_WRAP = false;
const DEFAULT_DIFF_COLLAPSED = true;
const DEFAULT_DIFF_SPLIT = false;
const DEFAULT_SHOW_ALL_MODELS = false;

const [diffWordWrap, setDiffWordWrap] = createSignal(DEFAULT_DIFF_WORD_WRAP);
const [diffCollapsed, setDiffCollapsed] = createSignal(DEFAULT_DIFF_COLLAPSED);
const [diffSplit, setDiffSplit] = createSignal(DEFAULT_DIFF_SPLIT);
const [showAllModels, setShowAllModels] = createSignal(DEFAULT_SHOW_ALL_MODELS);

/** Reactive pref — wrap long lines in inline diff previews. */
export function diffWordWrapPref(): boolean {
  return diffWordWrap();
}

/** Reactive pref — start inline diff previews fully collapsed to the file header. */
export function diffCollapsedPref(): boolean {
  return diffCollapsed();
}

/** Reactive pref — render the fullscreen diff viewer side-by-side. */
export function diffSplitPref(): boolean {
  return diffSplit();
}

/** Reactive pref — list every eligible model in role pickers (skips role layer). */
export function showAllModelsPref(): boolean {
  return showAllModels();
}

export function resolveDiffWordWrap(prefs?: DenDisplayPrefs): boolean {
  return prefs?.diffWordWrap ?? DEFAULT_DIFF_WORD_WRAP;
}

export function resolveDiffCollapsed(prefs?: DenDisplayPrefs): boolean {
  return prefs?.diffCollapsed ?? DEFAULT_DIFF_COLLAPSED;
}

export function resolveDiffSplit(prefs?: DenDisplayPrefs): boolean {
  return prefs?.diffSplit ?? DEFAULT_DIFF_SPLIT;
}

export function resolveShowAllModels(prefs?: DenDisplayPrefs): boolean {
  return prefs?.showAllModels ?? DEFAULT_SHOW_ALL_MODELS;
}

export function syncDisplayPrefsFromSnapshot(): void {
  const display = getAppStateSnapshot().display;
  setDiffWordWrap(resolveDiffWordWrap(display));
  setDiffCollapsed(resolveDiffCollapsed(display));
  setDiffSplit(resolveDiffSplit(display));
  setShowAllModels(resolveShowAllModels(display));
}

async function patchDisplay(patch: DenDisplayPrefs): Promise<void> {
  await persistAppStateInBackground({
    display: {
      ...getAppStateSnapshot().display,
      ...patch,
    },
  });
}

export async function saveDiffWordWrap(value: boolean): Promise<void> {
  setDiffWordWrap(value);
  await patchDisplay({ diffWordWrap: value });
}

export async function saveDiffCollapsed(value: boolean): Promise<void> {
  setDiffCollapsed(value);
  await patchDisplay({ diffCollapsed: value });
}

export async function saveDiffSplit(value: boolean): Promise<void> {
  setDiffSplit(value);
  await patchDisplay({ diffSplit: value });
}

export async function saveShowAllModels(value: boolean): Promise<void> {
  setShowAllModels(value);
  await patchDisplay({ showAllModels: value });
}
