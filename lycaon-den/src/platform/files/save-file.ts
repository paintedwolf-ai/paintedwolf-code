import { nativePathDialog } from "./native-path-dialog.ts";
import { isTauriRuntime } from "../runtime.ts";

async function writeBytesToGrant(grant: string, bytes: Uint8Array): Promise<void> {
  const { invoke } = await import("@tauri-apps/api/core");
  await invoke("save_bytes", { grant, bytes: Array.from(bytes) });
}

export type DownloadExportResult =
  | { kind: "saved"; path: string }
  | { kind: "cancelled" };

/** Canceling the native save dialog returns null. */
export async function saveBlobWithDialog(
  blob: Blob,
  filename: string,
): Promise<string | null> {
  const picked = await nativePathDialog("Save export", {
    kind: "save",
    fileName: filename,
  });
  if (picked == null) return null;
  if (picked.grant == null) {
    throw new Error("save panel returned no write grant");
  }
  const bytes = new Uint8Array(await blob.arrayBuffer());
  await writeBytesToGrant(picked.grant, bytes);
  return picked.path;
}

function downloadExportViaAnchor(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.style.display = "none";
  document.body.appendChild(anchor);
  anchor.click();
  // Keep the blob URL alive until the download starts.
  window.setTimeout(() => {
    anchor.remove();
    URL.revokeObjectURL(url);
  }, 2_000);
}

/** Desktop exports use the native save dialog. */
export async function downloadExport(
  blob: Blob,
  filename: string,
): Promise<DownloadExportResult> {
  if (isTauriRuntime()) {
    const path = await saveBlobWithDialog(blob, filename);
    if (path == null) return { kind: "cancelled" };
    return { kind: "saved", path };
  }
  downloadExportViaAnchor(blob, filename);
  // Browser download has no absolute path; surface the suggested filename.
  return { kind: "saved", path: filename };
}
