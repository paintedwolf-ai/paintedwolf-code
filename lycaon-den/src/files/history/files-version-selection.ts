import { createSignal } from "solid-js";
import type { FileVersionView } from "./file-version.ts";
import type { FileBufferKey } from "../components/project-files-model.ts";
import type { DenEditorVersionComparison } from "../../../shared/app-state-types.ts";
import { editorVersionComparisonPref } from "../../settings/editor/editor-prefs.ts";

const [selections, setSelections] = createSignal(
  new Map<string, FileVersionView>(),
);

const [versionComparisons, setVersionComparisons] = createSignal(
  new Map<string, DenEditorVersionComparison>(),
);

function id(projectId: string, key: FileBufferKey): string {
  return `${projectId}\0${key}`;
}

const pending = new Map<string, object>();

/** Path opens and newer history selections invalidate unfinished history reads. */
export function beginFileVersionSelection(projectId: string, key: FileBufferKey) {
  const at = id(projectId, key);
  const token = {};
  pending.set(at, token);
  return {
    current: () => pending.get(at) === token,
    finish: () => { if (pending.get(at) === token) pending.delete(at); },
  };
}

/** The version a buffer shows, or null for the working file. */
export function selectedFileVersion(
  projectId: string,
  key: FileBufferKey,
): FileVersionView | null {
  return selections().get(id(projectId, key)) ?? null;
}

/** The comparison mode for historical inspection, or null when using default. */
export function selectedVersionComparison(
  projectId: string,
  key: FileBufferKey,
): DenEditorVersionComparison | null {
  return versionComparisons().get(id(projectId, key)) ?? null;
}

export function setSelectedVersionComparison(
  projectId: string,
  key: FileBufferKey,
  comparison: DenEditorVersionComparison | null,
): void {
  setVersionComparisons((current) => {
    const at = id(projectId, key);
    if (!comparison && !current.has(at)) return current;
    const next = new Map(current);
    if (comparison) next.set(at, comparison);
    else next.delete(at);
    return next;
  });
}

/** The comparison a displayed version paints: the chosen one, else the version's own, else the device default. */
export function shownVersionComparison(
  projectId: string,
  key: FileBufferKey,
  version: FileVersionView,
): DenEditorVersionComparison {
  return selectedVersionComparison(projectId, key) ?? version.initialComparison ?? editorVersionComparisonPref();
}

/** Whether a version has an earlier side to compare against. */
export function versionComparesBefore(version: FileVersionView): boolean {
  return version.beforeAvailability === "available" || version.beforeAvailability === "absent";
}

/** Flips the shown comparison; a version with no earlier side stays on Current. */
export function toggleVersionComparison(
  projectId: string,
  key: FileBufferKey,
  version: FileVersionView,
): DenEditorVersionComparison {
  const shown = shownVersionComparison(projectId, key, version);
  const next: DenEditorVersionComparison = shown === "before" || !versionComparesBefore(version) ? "current" : "before";
  setSelectedVersionComparison(projectId, key, next);
  return next;
}

/** Null returns the buffer to its working file; a version's own comparison holds while browsing history. */
export function setSelectedFileVersion(
  projectId: string,
  key: FileBufferKey,
  version: FileVersionView | null,
): void {
  pending.delete(id(projectId, key));
  if (!version) {
    setSelectedVersionComparison(projectId, key, null);
  } else if (version.initialComparison && !selectedVersionComparison(projectId, key)) {
    setSelectedVersionComparison(projectId, key, version.initialComparison);
  }
  setSelections((current) => {
    const at = id(projectId, key);
    if (!version && !current.has(at)) return current;
    const next = new Map(current);
    if (version) next.set(at, version);
    else next.delete(at);
    return next;
  });
}

export function resetFileVersionSelectionForTests(): void {
  pending.clear();
  setSelections(new Map());
  setVersionComparisons(new Map());
}

