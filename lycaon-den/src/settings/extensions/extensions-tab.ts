export type ExtensionsTab = "packs" | "units" | "desired" | "settings" | "model";

export type ExtensionsSurface = "settings" | "project";

const DEFAULT_TAB: Record<ExtensionsSurface, ExtensionsTab> = {
  settings: "model",
  project: "units",
};

const lastTabBySurface: Record<ExtensionsSurface, ExtensionsTab> = {
  settings: DEFAULT_TAB.settings,
  project: DEFAULT_TAB.project,
};

export function rememberedExtensionsTab(
  surface: ExtensionsSurface,
): ExtensionsTab {
  return lastTabBySurface[surface];
}

export function rememberExtensionsTab(
  surface: ExtensionsSurface,
  tab: ExtensionsTab,
): void {
  lastTabBySurface[surface] = tab;
}

export function resetExtensionsTabMemoryForTest(): void {
  lastTabBySurface.settings = DEFAULT_TAB.settings;
  lastTabBySurface.project = DEFAULT_TAB.project;
}
