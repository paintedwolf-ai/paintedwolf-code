/** Flushes pending viewport state when the window hides or closes. */

import { watchWindowExit } from "../../platform/windows/watch-window-exit.ts";
import { flushFilesTreeViewToDisk } from "./files-tree-view-state.ts";

export function watchWindowExitForFilesTreeView(): () => void {
  return watchWindowExit(flushFilesTreeViewToDisk);
}
