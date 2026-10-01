import { createSignal } from "solid-js";
import {
  windowSubject,
  type WindowSubject,
} from "../../platform/windows/window-subject.ts";

export function filesTreeStartsCollapsed(
  subject: WindowSubject | null = windowSubject(),
): boolean {
  return subject?.kind === "file";
}

// Window state survives Files stage unmounts.
export const [filesTreeCollapsed, setFilesTreeCollapsed] = createSignal(filesTreeStartsCollapsed());
/** Keeps the navigator open across its responsive breakpoint until the viewport changes. */
export const [filesTreeClaimed, setFilesTreeClaimed] = createSignal(false);

export function resetFilesTreeWindowStateForTests(): void {
  setFilesTreeCollapsed(false);
  setFilesTreeClaimed(false);
}
