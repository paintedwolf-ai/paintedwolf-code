/** Flushes hot-exit state when the window hides or closes. */

import { watchWindowExit } from "../../platform/windows/watch-window-exit.ts";
import { flushFilesHotExitToDisk } from "./files-hot-exit.ts";

export function watchWindowExitForHotExit(): () => void {
  return watchWindowExit(flushFilesHotExitToDisk);
}
