import { abandonFileTabTransfer, listenFileTabTransfer, readyFileTabTransfer, type FileTabTransferSettled } from "../../platform/windows/item-windows.ts";
import { publishEditorDocumentDraft } from "../documents/editor-document.ts";
import { flushFilesDraftSyncFor } from "../documents/files-draft-sync.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { scheduleSessionFidelityPersist } from "../editor/editor-session-fidelity.ts";
import { type FileBufferKey } from "../components/project-files-model.ts";
import type { FilesScope } from "../components/files-scope.ts";

type TransferDependencies = Pick<FilesScope, "projectId" | "closeBuffer"> & {
  onFileTabMovedOut: () => ((key: FileBufferKey) => void | Promise<void>) | undefined;
};

/**
 * Tabs this window carries out with native drags, by shell token. A tab stays
 * here until the shell reports that another window owns it; the shell holds
 * delivery until this window has published the tab's latest draft.
 */
export function createFilesTabTransfers({ projectId, closeBuffer, onFileTabMovedOut }: TransferDependencies) {
  const outgoing = new Map<string, FileBufferKey>();
  // A drag can settle before its token reaches this window.
  const settledEarly = new Map<string, FileTabTransferSettled["outcome"]>();

  const moveOut = async (key: FileBufferKey) => {
    const released = closeBuffer(key);
    if (!released || (await released) === "restored") return;
    scheduleSessionFidelityPersist();
    void onFileTabMovedOut()?.(key);
  };

  const settle = ({ token, outcome }: FileTabTransferSettled) => {
    const key = outgoing.get(token);
    if (!key) {
      settledEarly.set(token, outcome);
      return;
    }
    outgoing.delete(token);
    if (outcome === "committed") void moveOut(key);
  };

  const handOff = async (token: string, key: FileBufferKey) => {
    try {
      flushFilesDraftSyncFor(projectId(), key);
      const buffer = projectFilesState(projectId()).byKey[key];
      if (!buffer) throw new Error("The dragged file tab is no longer open.");
      if (buffer.dirty && !(await publishEditorDocumentDraft(projectId(), buffer))) {
        throw new Error("The latest file draft could not be handed off.");
      }
      await readyFileTabTransfer(token);
    } catch (err) {
      console.debug("[item-window] native tab transfer abandoned", err);
      await abandonFileTabTransfer(token).catch((abandonErr) => console.debug("[item-window] abandon tab transfer failed", abandonErr));
    }
  };

  /** Starts the native drag while the pointer button is still held. */
  const start = (transfer: (bufferKey: string) => Promise<string>, key: FileBufferKey) => {
    void transfer(key).then(
      (token) => {
        outgoing.set(token, key);
        const early = settledEarly.get(token);
        if (early) {
          settledEarly.delete(token);
          settle({ token, outcome: early });
          return;
        }
        void handOff(token, key);
      },
      (err) => console.debug("[item-window] native tab drag failed to start", err),
    );
  };

  return { start, observe: () => listenFileTabTransfer(settle) };
}
