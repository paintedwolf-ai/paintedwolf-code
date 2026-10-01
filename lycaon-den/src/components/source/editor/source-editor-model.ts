import { LycaonApiError } from "../../../api/http.ts";
import type { SourceEncoding } from "../../../api/types.ts";

import type { FileBufferKind } from "../../../files/documents/project-files-buffer-kind.ts";

/** Whether the in-app viewer may edit + save this source load. */
export function canEditLoadedSource(args: {
  kind?: FileBufferKind;
  overLimit?: boolean;
  sha256?: string | null;
  encoding: SourceEncoding | null;
  /** Worker overlays are read-only; saves target project roots. */
  jobId?: string | null;
  /** Host-reported owner-write bits; false opens as a viewer. */
  writable?: boolean | null;
}): boolean {
  if (args.kind && args.kind !== "text") return false;
  if (args.overLimit) return false;
  if ((args.jobId ?? "").trim()) return false;
  if (args.writable === false) return false;
  return Boolean((args.sha256 ?? "").trim() && args.encoding);
}

export function formatSourceMtime(iso: string | null | undefined): string {
  const raw = (iso ?? "").trim();
  if (!raw) return "—";
  const d = new Date(raw);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

export function formatSourceMimeLabel(mime: string | null | undefined): string {
  const m = (mime ?? "").trim();
  if (!m) return "Unknown";
  if (m.includes("/")) return m;
  return m;
}

export function formatSourceBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return "—";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10_240 ? 1 : 0)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

export function sourceSaveErrorMessage(err: unknown): string {
  if (err instanceof LycaonApiError) {
    if (err.code === "source_write_conflict" || err.code === "editor_revision_conflict") {
      return (
        err.message ||
        "File changed on disk since it was loaded. Reload, then re-apply your edit."
      );
    }
    if (err.message.trim()) return err.message.trim();
  }
  if (err instanceof Error && err.message.trim()) return err.message.trim();
  return "Failed to save file.";
}

export function isSourceWriteConflict(err: unknown): boolean {
  return err instanceof LycaonApiError && (
    err.code === "source_write_conflict" ||
    err.code === "editor_revision_conflict"
  );
}
