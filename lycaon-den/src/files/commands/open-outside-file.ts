import type { LycaonClient } from "../../api/client.ts";
import { reportSurfaceFailure } from "../../notices/surface-failure.ts";
import { nativePathDialog } from "../../platform/files/native-path-dialog.ts";
import { openFilesBuffer } from "../documents/project-files-buffer-lifecycle.ts";
import { refreshChatLocations } from "../source/chat-locations.ts";
import { OUTSIDE_ROOT_LABEL, recordOutsideRoot } from "../source/outside-namespace.ts";

const OPEN_OUTSIDE_FAILED = {
  code: "files_open_outside_failed",
  title: "Couldn’t open file",
  suggestedAction: "Choose the file again.",
};

export async function openOutsideFile(client: LycaonClient | null | undefined, projectId: string, chatId: string | undefined): Promise<void> {
  if (!client || !chatId) return;
  const picked = await nativePathDialog("Open file outside the project", { kind: "file" });
  if (!picked) return;
  try {
    const opened = await client.openOutsideFile(projectId, { session_id: chatId, path: picked.path });
    recordOutsideRoot(projectId, opened.root_id);
    refreshChatLocations(projectId);
    openFilesBuffer(projectId, {
      rootId: opened.root_id, rootLabel: OUTSIDE_ROOT_LABEL, fileId: opened.file_id, path: opened.path, intent: "permanent",
    });
  } catch (error) {
    reportSurfaceFailure(OPEN_OUTSIDE_FAILED, error instanceof Error ? error.message : "The file could not be opened.", projectId);
  }
}
