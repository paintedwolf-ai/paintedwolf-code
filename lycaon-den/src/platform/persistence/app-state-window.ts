import { flushComposerDocumentsToDisk } from "../../chat/composer/composer-document-store.ts";
import { watchWindowExit } from "../windows/watch-window-exit.ts";

/** Flushes the shared durability queue when this window stops being active. */
export function watchWindowExitForAppState(): () => void {
  return watchWindowExit(flushComposerDocumentsToDisk);
}
