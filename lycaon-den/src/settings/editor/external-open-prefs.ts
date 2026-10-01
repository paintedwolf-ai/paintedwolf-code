import { createSignal } from "solid-js";
import type {
  BrowserPreset,
  DenExternalOpenPrefs,
  ExternalEditorPreset,
} from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";

export const DEFAULT_BROWSER: BrowserPreset = "system-default";
export const DEFAULT_EXTERNAL_EDITOR: ExternalEditorPreset = "cursor";

const EDITOR_PRESETS = new Set<string>([
  "cursor",
  "vscode",
  "vscode-insiders",
  "vscodium",
  "windsurf",
  "sublime",
  "bbedit",
  "textmate",
  "coteditor",
  "nova",
  "zed",
  "xcode",
  "intellij-idea",
  "webstorm",
  "pycharm",
  "goland",
  "rubymine",
  "phpstorm",
  "clion",
  "android-studio",
  "rider",
  "macvim",
  "neovide",
  "custom",
]);
const BROWSER_PRESETS = new Set<string>([
  "system-default",
  "chrome",
  "firefox",
  "safari",
  "custom",
]);

/** Labels saved presets even when detection cannot resolve them. */
export const EDITOR_LABELS: Record<ExternalEditorPreset, string> = {
  cursor: "Cursor",
  vscode: "VS Code",
  "vscode-insiders": "VS Code Insiders",
  vscodium: "VSCodium",
  windsurf: "Windsurf",
  sublime: "Sublime Text",
  bbedit: "BBEdit",
  textmate: "TextMate",
  coteditor: "CotEditor",
  nova: "Nova",
  zed: "Zed",
  xcode: "Xcode",
  "intellij-idea": "IntelliJ IDEA",
  webstorm: "WebStorm",
  pycharm: "PyCharm",
  goland: "GoLand",
  rubymine: "RubyMine",
  phpstorm: "PhpStorm",
  clion: "CLion",
  "android-studio": "Android Studio",
  rider: "Rider",
  macvim: "MacVim",
  neovide: "Neovide",
  custom: "Custom",
};

export function isExternalEditorPreset(
  value: string,
): value is ExternalEditorPreset {
  return EDITOR_PRESETS.has(value);
}

export function isBrowserPreset(value: string): value is BrowserPreset {
  return BROWSER_PRESETS.has(value);
}

/** Fill D-table defaults for missing keys. */
export function resolveExternalOpenPrefs(
  prefs?: DenExternalOpenPrefs,
): Required<
  Pick<DenExternalOpenPrefs, "externalEditor" | "browser">
> &
  Pick<DenExternalOpenPrefs, "customOpenCommand" | "customOpenFolderCommand" | "customBrowserCommand"> {
  const externalEditor =
    prefs?.externalEditor && isExternalEditorPreset(prefs.externalEditor)
      ? prefs.externalEditor
      : DEFAULT_EXTERNAL_EDITOR;
  const browser =
    prefs?.browser && isBrowserPreset(prefs.browser)
      ? prefs.browser
      : DEFAULT_BROWSER;
  return {
    externalEditor,
    browser,
    ...(typeof prefs?.customOpenCommand === "string"
      ? { customOpenCommand: prefs.customOpenCommand }
      : {}),
    ...(typeof prefs?.customOpenFolderCommand === "string"
      ? { customOpenFolderCommand: prefs.customOpenFolderCommand } : {}),
    ...(typeof prefs?.customBrowserCommand === "string"
      ? { customBrowserCommand: prefs.customBrowserCommand }
      : {}),
  };
}

/** Drop unknown enums; keep only recognized keys for persistence. */
export function normalizeExternalOpenPrefs(
  prefs: DenExternalOpenPrefs,
): DenExternalOpenPrefs | undefined {
  const next: DenExternalOpenPrefs = {};
  if (prefs.externalEditor && isExternalEditorPreset(prefs.externalEditor)) {
    next.externalEditor = prefs.externalEditor;
  }
  if (prefs.browser && isBrowserPreset(prefs.browser)) {
    next.browser = prefs.browser;
  }
  if (typeof prefs.customOpenCommand === "string") {
    next.customOpenCommand = prefs.customOpenCommand;
  }
  if (typeof prefs.customOpenFolderCommand === "string") {
    next.customOpenFolderCommand = prefs.customOpenFolderCommand;
  }
  if (typeof prefs.customBrowserCommand === "string") {
    next.customBrowserCommand = prefs.customBrowserCommand;
  }
  return Object.keys(next).length > 0 ? next : undefined;
}

const [resolved, setResolved] = createSignal(
  resolveExternalOpenPrefs(undefined),
);

/** Reactive resolved prefs (defaults applied). */
export function externalOpenPrefs() {
  return resolved();
}

export function syncExternalOpenPrefsFromSnapshot(): void {
  setResolved(resolveExternalOpenPrefs(getAppStateSnapshot().externalOpen));
}

export async function saveExternalOpenPrefs(
  patch: DenExternalOpenPrefs,
): Promise<void> {
  const merged = normalizeExternalOpenPrefs({
    ...getAppStateSnapshot().externalOpen,
    ...patch,
  });
  setResolved(resolveExternalOpenPrefs(merged));
  await persistAppStateInBackground({ externalOpen: merged });
}

/** Vitest-only — reset module signal between tests. */
export function resetExternalOpenPrefsForTests(
  prefs?: DenExternalOpenPrefs,
): void {
  setResolved(resolveExternalOpenPrefs(prefs));
}
