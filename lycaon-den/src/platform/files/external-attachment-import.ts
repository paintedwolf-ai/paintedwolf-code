/** Native-drop import of an external file as an immutable attachment snapshot. */

import { isTauriRuntime } from "../runtime.ts";
import type { AttachmentUploadResponse } from "../../api/types.ts";

export async function importExternalAttachment(
  projectId: string,
  absPath: string,
): Promise<AttachmentUploadResponse> {
  if (!isTauriRuntime()) {
    throw new Error("requires the desktop app");
  }
  const { invoke } = await import("@tauri-apps/api/core");
  return invoke<AttachmentUploadResponse>("import_external_attachment", {
    projectId,
    absPath,
  });
}
