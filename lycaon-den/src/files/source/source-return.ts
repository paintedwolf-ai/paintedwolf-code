/** A routed visit retains the exact editor line it came from. */
import { createSignal } from "solid-js";
import { revealFilesBuffer } from "../documents/project-files-buffers.ts";
import type { JumpEntry } from "../history/jump-history.ts";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";

export type SourceReturn = JumpEntry & { projectId: string };
const [sourceReturn, setSourceReturn] = createSignal<SourceReturn | null>(null);
export { sourceReturn };
export function rememberSourceReturn(entry: SourceReturn): void { setSourceReturn(entry); }
export function clearSourceReturn(): void { setSourceReturn(null); }
export async function returnToSource(): Promise<boolean> {
  const entry = sourceReturn();
  if (!entry) return false;
  if (revealFilesBuffer(entry.projectId, entry.bufferKey, entry.line)) {
    clearSourceReturn();
    return true;
  }
  const result = await openSourceLocation({ ...entry, intent: "transient" });
  const opened = result.status === "opened-in-app";
  if (sourceReturn() !== entry) return false;
  if (opened) clearSourceReturn();
  return opened;
}
