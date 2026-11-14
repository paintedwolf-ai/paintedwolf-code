/** Formatting settings the host resolved from each tool's configuration. */

import type { FormatSettings, FormatSettingsSource, FormatTool } from "../../../api/types.ts";

export type FormatSources = readonly FormatSettingsSource[];

const EDITORCONFIG = ".editorconfig";

export function editorConfigSettings(sources: FormatSources): FormatSettings | undefined {
  return sources.find((source) => source.tool === "editorconfig")?.settings;
}

function configDepth(source: FormatSettingsSource): number {
  const nearest = source.configs[0] ?? "";
  return nearest.split("/").length;
}

/**
 * The line width a file is formatted to: the nearest configured formatter of
 * its language, else EditorConfig. Formatters at the same depth keep `tools` order.
 */
export function declaredLineWidth(sources: FormatSources | undefined, tools: readonly FormatTool[]): number | undefined {
  if (!sources) return undefined;
  let best: FormatSettingsSource | undefined;
  for (const tool of tools) {
    const source = sources.find((s) => s.tool === tool && s.settings.line_width);
    if (source && (!best || configDepth(source) > configDepth(best))) best = source;
  }
  return best?.settings.line_width ?? editorConfigSettings(sources)?.line_width;
}

const configNamesByProject = new Map<string, ReadonlySet<string>>();

/** Records the configuration file names the host reads for a project. */
export function rememberFormatConfigNames(projectId: string, names: readonly string[]): void {
  configNamesByProject.set(projectId, new Set([EDITORCONFIG, ...names]));
}

/** Whether a change to this path can change a project's formatting settings. */
export function isFormatConfigPath(projectId: string, path: string | undefined): boolean {
  if (!path) return false;
  const name = path.slice(path.lastIndexOf("/") + 1);
  return (configNamesByProject.get(projectId) ?? new Set([EDITORCONFIG])).has(name);
}

export function resetFormatConfigNamesForTests(): void {
  configNamesByProject.clear();
}
