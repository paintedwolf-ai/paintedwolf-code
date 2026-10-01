import { lycaonFetch, requireSuccessfulResponse } from "../../api/http.ts";
import type { BackendConnection } from "../../platform/connection/backend.ts";
import { downloadExport } from "../../platform/files/save-file.ts";

export function diagnosticsBundleFilename(now: Date): string {
  const stamp = now
    .toISOString()
    .replace(/[-:]/g, "")
    .replace(/\..*$/, "")
    .replace("T", "-");
  return `painted-wolf-code-diagnostics-${stamp}.zip`;
}

export type SaveBundleResult =
  | { kind: "saved"; path: string }
  | { kind: "cancelled" }
  | { kind: "failed"; detail: string };

export async function saveDiagnosticsBundle(
  connection: BackendConnection,
  now: Date = new Date(),
): Promise<SaveBundleResult> {
  try {
    const res = await requireSuccessfulResponse(await lycaonFetch(connection, "/v1/diagnostics/export"));
    const blob = await res.blob();
    const download = await downloadExport(blob, diagnosticsBundleFilename(now));
    if (download.kind === "cancelled") return { kind: "cancelled" };
    return { kind: "saved", path: download.path };
  } catch (err) {
    return {
      kind: "failed",
      detail: err instanceof Error ? err.message : String(err),
    };
  }
}

/** Exports diagnostics without opening the store. */
export async function saveStartupDiagnosticsBundle(
  now: Date = new Date(),
): Promise<SaveBundleResult> {
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    const bytes = await invoke<number[]>("export_startup_diagnostics");
    if (!Array.isArray(bytes) || bytes.length === 0) {
      return { kind: "failed", detail: "startup report was empty" };
    }
    const blob = new Blob([Uint8Array.from(bytes)], {
      type: "application/zip",
    });
    const download = await downloadExport(blob, diagnosticsBundleFilename(now));
    if (download.kind === "cancelled") return { kind: "cancelled" };
    return { kind: "saved", path: download.path };
  } catch (err) {
    return {
      kind: "failed",
      detail: err instanceof Error ? err.message : String(err),
    };
  }
}
