import { createSignal } from "solid-js";
import { DEN_FILES_TREE_LEVELS } from "../../../shared/app-state-types.ts";
import type {
  DenEditorAgentActivityKinds,
  DenEditorIndentStyle,
  DenEditorPrefs,
  DenEditorKeymap,
  DenEditorVersionComparison,
  DenWalkControlsLocation,
  DenWalkControlsPlacement,
} from "../../../shared/app-state-types.ts";
import type { OverviewTickKinds } from "../../components/source/annotations/overview-ruler-model.ts";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";
import {
  bundledFontsInRole,
  lookupBundledFont,
  sanitizeFontFamily,
} from "../../fonts/font-catalog.ts";

const DEFAULT_EDITOR_WORD_WRAP = false;
const DEFAULT_EDITOR_LINE_NUMBERS = true;
const DEFAULT_EDITOR_FONT_SIZE = 13;
const DEFAULT_EDITOR_INDENT_GUIDES = true;
const DEFAULT_EDITOR_WHITESPACE = false;
const DEFAULT_EDITOR_INDENT_STYLE: DenEditorIndentStyle = "spaces";
const DEFAULT_EDITOR_INDENT_WIDTH = 4;
const DEFAULT_EDITOR_FONT_FAMILY = "default";
const DEFAULT_EDITOR_LINE_HEIGHT = 1.5;
const DEFAULT_EDITOR_VERSION_COMPARISON: DenEditorVersionComparison = "current";
const DEFAULT_EDITOR_SCROLLBAR_TICKS = true;
const DEFAULT_EDITOR_SCROLLBAR_TICK_KIND = true;
const NO_SCROLLBAR_TICKS: OverviewTickKinds = {
  symbols: false,
  changes: false,
  matches: false,
  cursor: false,
  windows: false,
  agents: false,
};
const DEFAULT_EDITOR_AGENT_ACTIVITY = true;
const DEFAULT_EDITOR_AGENT_ACTIVITY_KIND = true;

export type EditorScrollbarTickKind = keyof OverviewTickKinds;
export type AgentActivityKinds = Required<DenEditorAgentActivityKinds>;
export type EditorAgentActivityKind = keyof AgentActivityKinds;
const NO_AGENT_ACTIVITY: AgentActivityKinds = { reads: false, changes: false, fileNames: false };

/** Mono families available in the app bundle. */
export const DEN_EDITOR_FONT_FAMILY_OPTIONS = [
  { value: "default", label: "Match app font" },
  ...bundledFontsInRole("mono").map((f) => ({ value: f.family, label: f.family })),
] as const satisfies ReadonlyArray<{ value: string; label: string }>;

export const DEN_EDITOR_PREF_KEYS = [
  "keymap",
  "tabMovesFocus",
  "wordWrap",
  "revealInTree",
  "treeVisibleLevels",
  "lineNumbers",
  "fontSize",
  "indentGuides",
  "whitespace",
  "indentStyle",
  "indentWidth",
  "fontFamily",
  "lineHeight",
  "versionComparison",
  "walkControlsDefault",
  "walkControlsLocation",
  "scrollbarTicks",
  "scrollbarTickKinds",
  "agentActivity",
  "agentActivityKinds",
] as const satisfies ReadonlyArray<keyof DenEditorPrefs>;

const [revealInTree, setRevealInTree] = createSignal(true);
const [treeVisibleLevels, setTreeVisibleLevels] = createSignal<number>(DEN_FILES_TREE_LEVELS.default);
export const editorTreeVisibleLevelsPref = treeVisibleLevels;

export function resolveEditorTreeVisibleLevels(prefs?: DenEditorPrefs): number {
  const value = prefs?.treeVisibleLevels;
  return typeof value === "number" && Number.isInteger(value) &&
    (value === 0 || value >= DEN_FILES_TREE_LEVELS.min && value <= DEN_FILES_TREE_LEVELS.max)
    ? value : DEN_FILES_TREE_LEVELS.default;
}

export async function saveEditorTreeVisibleLevels(value: number): Promise<void> {
  const next = resolveEditorTreeVisibleLevels({ treeVisibleLevels: value });
  setTreeVisibleLevels(next);
  await patchEditor({ treeVisibleLevels: next });
}
const [editorKeymapPref, setEditorKeymapPref] = createSignal<DenEditorKeymap>("emacs");
const [editorTabMovesFocusPref, setEditorTabMovesFocusPref] = createSignal(false);
export { editorKeymapPref, editorTabMovesFocusPref };
export function resolveEditorKeymap(prefs?: DenEditorPrefs): DenEditorKeymap {
  return prefs?.keymap === "vim" ? "vim" : "emacs";
}
export async function saveEditorKeymap(value: DenEditorKeymap): Promise<void> {
  setEditorKeymapPref(value);
  await patchEditor({ keymap: value });
}
export async function saveEditorTabMovesFocus(value: boolean): Promise<void> {
  setEditorTabMovesFocusPref(value);
  await patchEditor({ tabMovesFocus: value });
}
const [wordWrap, setWordWrap] = createSignal(DEFAULT_EDITOR_WORD_WRAP);
const [lineNumbers, setLineNumbers] = createSignal(DEFAULT_EDITOR_LINE_NUMBERS);
const [fontSize, setFontSize] = createSignal(DEFAULT_EDITOR_FONT_SIZE);
const [indentGuides, setIndentGuides] = createSignal(DEFAULT_EDITOR_INDENT_GUIDES);
const [whitespace, setWhitespace] = createSignal(DEFAULT_EDITOR_WHITESPACE);
const [indentStyle, setIndentStyle] = createSignal<DenEditorIndentStyle>(
  DEFAULT_EDITOR_INDENT_STYLE,
);
const [indentWidth, setIndentWidth] = createSignal(DEFAULT_EDITOR_INDENT_WIDTH);
const [fontFamily, setFontFamily] = createSignal(DEFAULT_EDITOR_FONT_FAMILY);
const [lineHeight, setLineHeight] = createSignal(DEFAULT_EDITOR_LINE_HEIGHT);
const [versionComparison, setVersionComparison] =
  createSignal<DenEditorVersionComparison>(DEFAULT_EDITOR_VERSION_COMPARISON);
const [walkControlsDefault, setWalkControlsDefault] = createSignal<DenWalkControlsPlacement>("floating");
const [walkControlsLocation, setWalkControlsLocation] = createSignal<DenWalkControlsLocation>();

export const editorWalkControlsDefaultPref = walkControlsDefault;
export const editorWalkControlsLocationPref = walkControlsLocation;
export function editorWalkControlsPlacement(): DenWalkControlsLocation {
  return walkControlsLocation() ?? { placement: walkControlsDefault() };
}
export async function saveEditorWalkControlsDefault(value: DenWalkControlsPlacement): Promise<void> {
  setWalkControlsDefault(value);
  setWalkControlsLocation(undefined);
  await patchEditor({ walkControlsDefault: value, walkControlsLocation: undefined });
}
export async function saveEditorWalkControlsLocation(value?: DenWalkControlsLocation): Promise<void> {
  setWalkControlsLocation(value);
  await patchEditor({ walkControlsLocation: value });
}

const [scrollbarTicks, setScrollbarTicks] = createSignal(
  DEFAULT_EDITOR_SCROLLBAR_TICKS,
);
const [scrollbarTickKinds, setScrollbarTickKinds] = createSignal<OverviewTickKinds>(
  resolveEditorScrollbarTickKinds(undefined),
);
const [agentActivity, setAgentActivity] = createSignal(DEFAULT_EDITOR_AGENT_ACTIVITY);
const [agentActivityKinds, setAgentActivityKinds] = createSignal<AgentActivityKinds>(
  resolveEditorAgentActivityKinds(undefined),
);

export function editorRevealInTreePref(): boolean {
  return revealInTree();
}
export function editorWordWrapPref(): boolean {
  return wordWrap();
}
export function editorLineNumbersPref(): boolean {
  return lineNumbers();
}
export function editorFontSizePref(): number {
  return fontSize();
}
export function editorIndentGuidesPref(): boolean {
  return indentGuides();
}
export function editorWhitespacePref(): boolean {
  return whitespace();
}
export function editorIndentStylePref(): DenEditorIndentStyle {
  return indentStyle();
}
export function editorIndentWidthPref(): number {
  return indentWidth();
}
export function editorFontFamilyPref(): string {
  return fontFamily();
}
export function editorLineHeightPref(): number {
  return lineHeight();
}
export function editorVersionComparisonPref(): DenEditorVersionComparison {
  return versionComparison();
}
export function editorScrollbarTicksPref(): boolean {
  return scrollbarTicks();
}
export function editorScrollbarTickKindsPref(): OverviewTickKinds {
  return scrollbarTickKinds();
}
/** Kinds the ruler paints; the master switch keeps the saved picks. */
export function editorShownScrollbarTicks(): OverviewTickKinds {
  return scrollbarTicks() ? scrollbarTickKinds() : NO_SCROLLBAR_TICKS;
}
export function editorAgentActivityPref(): boolean {
  return agentActivity();
}
export function editorAgentActivityKindsPref(): AgentActivityKinds {
  return agentActivityKinds();
}
/** Agent activity kinds shown; the master switch keeps the saved picks. */
export function editorShownAgentActivity(): AgentActivityKinds {
  return agentActivity() ? agentActivityKinds() : NO_AGENT_ACTIVITY;
}

export function resolveEditorWordWrap(prefs?: DenEditorPrefs): boolean {
  return prefs?.wordWrap ?? DEFAULT_EDITOR_WORD_WRAP;
}
export function resolveEditorLineNumbers(prefs?: DenEditorPrefs): boolean {
  return prefs?.lineNumbers ?? DEFAULT_EDITOR_LINE_NUMBERS;
}
export function resolveEditorFontSize(prefs?: DenEditorPrefs): number {
  const n = prefs?.fontSize;
  if (typeof n === "number" && Number.isInteger(n) && n >= 10 && n <= 20) return n;
  return DEFAULT_EDITOR_FONT_SIZE;
}
export function resolveEditorIndentGuides(prefs?: DenEditorPrefs): boolean {
  return prefs?.indentGuides ?? DEFAULT_EDITOR_INDENT_GUIDES;
}
export function resolveEditorWhitespace(prefs?: DenEditorPrefs): boolean {
  return prefs?.whitespace ?? DEFAULT_EDITOR_WHITESPACE;
}
export function resolveEditorIndentStyle(
  prefs?: DenEditorPrefs,
): DenEditorIndentStyle {
  return prefs?.indentStyle === "tabs" ? "tabs" : DEFAULT_EDITOR_INDENT_STYLE;
}
export function resolveEditorIndentWidth(prefs?: DenEditorPrefs): number {
  const w = prefs?.indentWidth;
  return w === 2 || w === 4 || w === 8 ? w : DEFAULT_EDITOR_INDENT_WIDTH;
}
export function resolveEditorFontFamily(prefs?: DenEditorPrefs): string {
  const raw = prefs?.fontFamily;
  if (typeof raw !== "string") return DEFAULT_EDITOR_FONT_FAMILY;
  if (raw.trim() === DEFAULT_EDITOR_FONT_FAMILY) return DEFAULT_EDITOR_FONT_FAMILY;
  return sanitizeFontFamily(raw) ?? DEFAULT_EDITOR_FONT_FAMILY;
}

/** Whether the selected font is bundled or provided by the app theme. */
export function editorFontIsGuaranteed(family: string): boolean {
  return (
    family === DEFAULT_EDITOR_FONT_FAMILY ||
    lookupBundledFont("mono", family) !== undefined
  );
}
export function resolveEditorLineHeight(prefs?: DenEditorPrefs): number {
  const n = prefs?.lineHeight;
  if (typeof n !== "number" || !Number.isFinite(n)) return DEFAULT_EDITOR_LINE_HEIGHT;
  const clamped = Math.min(2.0, Math.max(1.1, n));
  return Math.round(clamped * 20) / 20;
}
export function resolveEditorVersionComparison(
  prefs?: DenEditorPrefs,
): DenEditorVersionComparison {
  return prefs?.versionComparison === "before"
    ? "before"
    : DEFAULT_EDITOR_VERSION_COMPARISON;
}
export function resolveEditorScrollbarTicks(prefs?: DenEditorPrefs): boolean {
  return prefs?.scrollbarTicks ?? DEFAULT_EDITOR_SCROLLBAR_TICKS;
}
export function resolveEditorScrollbarTickKinds(
  prefs?: DenEditorPrefs,
): OverviewTickKinds {
  const saved = prefs?.scrollbarTickKinds;
  return {
    symbols: saved?.symbols ?? DEFAULT_EDITOR_SCROLLBAR_TICK_KIND,
    changes: saved?.changes ?? DEFAULT_EDITOR_SCROLLBAR_TICK_KIND,
    matches: saved?.matches ?? DEFAULT_EDITOR_SCROLLBAR_TICK_KIND,
    cursor: saved?.cursor ?? DEFAULT_EDITOR_SCROLLBAR_TICK_KIND,
    windows: saved?.windows ?? DEFAULT_EDITOR_SCROLLBAR_TICK_KIND,
    agents: saved?.agents ?? DEFAULT_EDITOR_SCROLLBAR_TICK_KIND,
  };
}
export function resolveEditorAgentActivity(prefs?: DenEditorPrefs): boolean {
  return prefs?.agentActivity ?? DEFAULT_EDITOR_AGENT_ACTIVITY;
}
export function resolveEditorAgentActivityKinds(prefs?: DenEditorPrefs): AgentActivityKinds {
  const saved = prefs?.agentActivityKinds;
  return {
    reads: saved?.reads ?? DEFAULT_EDITOR_AGENT_ACTIVITY_KIND,
    changes: saved?.changes ?? DEFAULT_EDITOR_AGENT_ACTIVITY_KIND,
    fileNames: saved?.fileNames ?? DEFAULT_EDITOR_AGENT_ACTIVITY_KIND,
  };
}

/** Builds a font stack ending with the app monospace token. */
export function cssFontFamilyForEditor(family: string): string {
  if (family.trim() === DEFAULT_EDITOR_FONT_FAMILY) return "var(--den-font-mono)";
  const name = sanitizeFontFamily(family);
  if (!name) return "var(--den-font-mono)";
  return `"${name}", var(--den-font-mono)`;
}

export function syncEditorPrefsFromSnapshot(): void {
  const editor = getAppStateSnapshot().editor;
  setRevealInTree(editor?.revealInTree ?? true);
  setTreeVisibleLevels(resolveEditorTreeVisibleLevels(editor));
  setEditorKeymapPref(resolveEditorKeymap(editor));
  setEditorTabMovesFocusPref(editor?.tabMovesFocus ?? false);
  setWordWrap(resolveEditorWordWrap(editor));
  setLineNumbers(resolveEditorLineNumbers(editor));
  setFontSize(resolveEditorFontSize(editor));
  setIndentGuides(resolveEditorIndentGuides(editor));
  setWhitespace(resolveEditorWhitespace(editor));
  setIndentStyle(resolveEditorIndentStyle(editor));
  setIndentWidth(resolveEditorIndentWidth(editor));
  setFontFamily(resolveEditorFontFamily(editor));
  setLineHeight(resolveEditorLineHeight(editor));
  setVersionComparison(resolveEditorVersionComparison(editor));
  setWalkControlsDefault(editor?.walkControlsDefault ?? "floating");
  setWalkControlsLocation(editor?.walkControlsLocation);
  setScrollbarTicks(resolveEditorScrollbarTicks(editor));
  setScrollbarTickKinds(resolveEditorScrollbarTickKinds(editor));
  setAgentActivity(resolveEditorAgentActivity(editor));
  setAgentActivityKinds(resolveEditorAgentActivityKinds(editor));
}

async function patchEditor(patch: DenEditorPrefs): Promise<void> {
  await persistAppStateInBackground({
    editor: {
      ...getAppStateSnapshot().editor,
      ...patch,
    },
  });
}

export async function saveEditorRevealInTree(value: boolean): Promise<void> {
  setRevealInTree(value);
  await patchEditor({ revealInTree: value });
}

export async function saveEditorWordWrap(value: boolean): Promise<void> {
  setWordWrap(value);
  await patchEditor({ wordWrap: value });
}
export async function saveEditorLineNumbers(value: boolean): Promise<void> {
  setLineNumbers(value);
  await patchEditor({ lineNumbers: value });
}
export async function saveEditorFontSize(value: number): Promise<void> {
  const clamped = Math.min(20, Math.max(10, Math.round(value)));
  setFontSize(clamped);
  await patchEditor({ fontSize: clamped });
}
export async function saveEditorIndentGuides(value: boolean): Promise<void> {
  setIndentGuides(value);
  await patchEditor({ indentGuides: value });
}
export async function saveEditorWhitespace(value: boolean): Promise<void> {
  setWhitespace(value);
  await patchEditor({ whitespace: value });
}
export async function saveEditorIndentStyle(
  value: DenEditorIndentStyle,
): Promise<void> {
  setIndentStyle(value);
  await patchEditor({ indentStyle: value });
}
export async function saveEditorIndentWidth(value: number): Promise<void> {
  const w = value === 2 || value === 8 ? value : 4;
  setIndentWidth(w);
  await patchEditor({ indentWidth: w });
}
export async function saveEditorFontFamily(value: string): Promise<void> {
  const next = resolveEditorFontFamily({ fontFamily: value });
  setFontFamily(next);
  await patchEditor({ fontFamily: next });
}
export async function saveEditorLineHeight(value: number): Promise<void> {
  const next = resolveEditorLineHeight({ lineHeight: value });
  setLineHeight(next);
  await patchEditor({ lineHeight: next });
}
export async function saveEditorVersionComparison(
  value: DenEditorVersionComparison,
): Promise<void> {
  const next = resolveEditorVersionComparison({ versionComparison: value });
  setVersionComparison(next);
  await patchEditor({ versionComparison: next });
}
export async function saveEditorScrollbarTicks(value: boolean): Promise<void> {
  setScrollbarTicks(value);
  await patchEditor({ scrollbarTicks: value });
}
export async function saveEditorScrollbarTickKind(
  kind: EditorScrollbarTickKind,
  value: boolean,
): Promise<void> {
  const next = { ...scrollbarTickKinds(), [kind]: value };
  setScrollbarTickKinds(next);
  await patchEditor({ scrollbarTickKinds: next });
}
export async function saveEditorAgentActivity(value: boolean): Promise<void> {
  setAgentActivity(value);
  await patchEditor({ agentActivity: value });
}
export async function saveEditorAgentActivityKind(
  kind: EditorAgentActivityKind,
  value: boolean,
): Promise<void> {
  const next = { ...agentActivityKinds(), [kind]: value };
  setAgentActivityKinds(next);
  await patchEditor({ agentActivityKinds: next });
}

export function resetEditorPrefsForTests(prefs?: DenEditorPrefs): void {
  setRevealInTree(prefs?.revealInTree ?? true);
  setTreeVisibleLevels(resolveEditorTreeVisibleLevels(prefs));
  setEditorKeymapPref(resolveEditorKeymap(prefs));
  setEditorTabMovesFocusPref(prefs?.tabMovesFocus ?? false);
  setWordWrap(resolveEditorWordWrap(prefs));
  setLineNumbers(resolveEditorLineNumbers(prefs));
  setFontSize(resolveEditorFontSize(prefs));
  setIndentGuides(resolveEditorIndentGuides(prefs));
  setWhitespace(resolveEditorWhitespace(prefs));
  setIndentStyle(resolveEditorIndentStyle(prefs));
  setIndentWidth(resolveEditorIndentWidth(prefs));
  setFontFamily(resolveEditorFontFamily(prefs));
  setLineHeight(resolveEditorLineHeight(prefs));
  setVersionComparison(resolveEditorVersionComparison(prefs));
  setWalkControlsDefault(prefs?.walkControlsDefault ?? "floating");
  setWalkControlsLocation(prefs?.walkControlsLocation);
  setScrollbarTicks(resolveEditorScrollbarTicks(prefs));
  setScrollbarTickKinds(resolveEditorScrollbarTickKinds(prefs));
  setAgentActivity(resolveEditorAgentActivity(prefs));
  setAgentActivityKinds(resolveEditorAgentActivityKinds(prefs));
}
