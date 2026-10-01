/** Pure model for the Files-stage "new file" / "new folder" affordances. */

import { LycaonApiError } from "../../api/http.ts";
import { childTreePath } from "../components/project-files-model.ts";

/** What the naming row is making. */
export type NewEntryKind = "file" | "folder";

/** A validated new-entry path, or why the typed name cannot become one. */
export type NewEntryPathResult = { path: string } | { error: string };

/** Resolves nested names within the selected folder; a trailing slash requires a directory. */
export function newEntryPath(
  dir: string,
  typed: string,
  kind: NewEntryKind,
): NewEntryPathResult {
  let name = typed.trim();
  if (!name) {
    return { error: kind === "folder" ? "Enter a folder name." : "Enter a file name." };
  }
  if (name.startsWith("/")) {
    return { error: "Enter a path relative to this folder." };
  }
  if (name.endsWith("/")) {
    if (kind === "file") return { error: "Enter a file name, not a folder." };
    name = name.replace(/\/+$/, "");
  }
  const segments = name.split("/");
  if (segments.some((s) => s.trim() === "")) {
    return { error: "Remove the empty path segment." };
  }
  if (segments.some((s) => s === "." || s === "..")) {
    return { error: "Paths cannot contain “.” or “..”." };
  }
  return { path: childTreePath(dir, name) };
}

/** User-facing failure copy for a refused create. */
export function sourceCreateErrorMessage(err: unknown): string {
  if (err instanceof LycaonApiError) {
    if (err.code === "source_already_exists") {
      return "A file or folder with that name already exists.";
    }
    if (err.message.trim()) return err.message.trim();
  }
  if (err instanceof Error && err.message.trim()) return err.message.trim();
  return "Could not create it.";
}

/**
 * Resolve an inline rename: the typed basename (no slash) under the same
 * parent directory as `fromPath`. Unchanged names return `{ unchanged: true }`.
 */
export function renameEntryPath(
  fromPath: string,
  typed: string,
): NewEntryPathResult | { unchanged: true } {
  const name = typed.trim();
  if (!name) return { error: "Enter a name." };
  if (name.includes("/") || name.includes("\\")) {
    return { error: "Rename cannot change folders — drag to move." };
  }
  if (name === "." || name === "..") {
    return { error: "Paths cannot contain “.” or “..”." };
  }
  const slash = fromPath.lastIndexOf("/");
  const parent = slash < 0 ? "." : fromPath.slice(0, slash) || ".";
  const currentName = slash < 0 ? fromPath : fromPath.slice(slash + 1);
  if (name === currentName) return { unchanged: true };
  return { path: childTreePath(parent, name) };
}

/** Select the basename excluding the extension (for rename preselection). */
export function renameSelectionRange(name: string): { start: number; end: number } {
  const dot = name.lastIndexOf(".");
  if (dot > 0) return { start: 0, end: dot };
  return { start: 0, end: name.length };
}
