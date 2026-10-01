import type { FileBriefingRequest } from "../../api/types.ts";
import { relativeTimeLabel } from "../../time/time-copy.ts";
import {
  fileVersionViewTitle,
  versionAfterSide,
  versionBeforeSide,
  type FileVersionView,
} from "../history/file-version.ts";
import { retainedDeletion } from "../history/last-deletion.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { sourceContentNotice, type ContentSide } from "../source/source-content-availability.ts";

export type FileBriefingTarget = Omit<FileBriefingRequest, "trigger">;
type FileBriefingSelectionBase = {
  displayPath: string | null;
  contextLabel: string | null;
};

export type FileBriefingSelection = FileBriefingSelectionBase & ({
  availability: "ready";
  target: FileBriefingTarget;
  unavailableReason: null;
} | {
  availability: "empty" | "loading" | "unavailable";
  target: null;
  unavailableReason: string;
});

type UnavailableSelection = FileBriefingSelectionBase & {
  availability: "empty" | "loading" | "unavailable";
  target: null;
  unavailableReason: string;
};

export function briefingSelectionForBuffer(
  buffer: FileBuffer | null,
  version: FileVersionView | null,
  now = Date.now(),
): FileBriefingSelection {
  if (!buffer) {
    return unavailable("Open a file to generate its summary.", null, null, "empty");
  }
  if (buffer.loading) {
    return unavailable("Loading this file…", buffer.path, null, "loading");
  }
  if (buffer.loadError) return unavailable(buffer.loadError, buffer.path);
  if (buffer.kind !== "text") {
    return unavailable(
      buffer.kind === "diff"
        ? "File summaries are unavailable for this preview."
        : buffer.kind === "walk"
        ? "File summaries are unavailable for a walk step."
        : "File summaries are available for readable text files.",
      buffer.path,
    );
  }
  if (version) return versionSelection(version, now);
  const deletion = retainedDeletion(buffer);
  if (deletion) {
    return { ...versionSelection(deletion.version, now, versionBeforeSide(deletion.version)), contextLabel: "Before deletion" };
  }
  const base = { root_id: buffer.rootId, path: buffer.path };
  if (buffer.dirty && buffer.documentId && buffer.documentRevision) {
    return available({
      ...base,
      presentation: "document",
      document_id: buffer.documentId,
      document_revision: buffer.documentRevision,
    });
  }
  if (buffer.jobId?.trim()) {
    return available({
      ...base,
      presentation: "current",
      worker_id: buffer.jobId.trim(),
    });
  }
  return available({ ...base, presentation: "current" });
}

function versionSelection(
  version: FileVersionView,
  now: number,
  side: ContentSide = versionAfterSide(version),
): FileBriefingSelection {
  const contextLabel = versionContextLabel(version, now);
  const notice = sourceContentNotice(side);
  if (notice) {
    return unavailable(
      notice.message,
      version.path,
      contextLabel,
    );
  }
  if (side.availability === "absent") {
    return unavailable("This file does not exist in this version.", version.path, contextLabel);
  }
  if (side.availability !== "available") {
    return unavailable(
      "Exact text for this version is unavailable.",
      version.path,
      contextLabel,
    );
  }
  // Git-backed states have no immutable ledger version id.
  if (!version.versionId.trim()) {
    return unavailable(
      "Summaries cover retained versions; this state comes from git history.",
      version.path,
      contextLabel,
    );
  }
  return {
    availability: "ready",
    target: {
      root_id: version.rootId,
      path: version.path,
      presentation: "version",
      version_id: version.versionId,
    },
    displayPath: version.path,
    unavailableReason: null,
    contextLabel,
  };
}

function versionContextLabel(version: FileVersionView, now: number): string {
  const relative = relativeTimeLabel(version.ts, now);
  return ["Version", fileVersionViewTitle(version), relative]
    .filter(Boolean)
    .join(" · ");
}

function available(target: FileBriefingTarget): FileBriefingSelection {
  return {
    availability: "ready",
    target,
    displayPath: target.path,
    unavailableReason: null,
    contextLabel: null,
  };
}

function unavailable(
  reason: string,
  displayPath: string | null = null,
  contextLabel: string | null = null,
  availability: UnavailableSelection["availability"] = "unavailable",
): UnavailableSelection {
  return {
    availability,
    target: null,
    displayPath,
    unavailableReason: reason,
    contextLabel,
  };
}

export function briefingTargetIdentity(target: FileBriefingTarget): string {
  return JSON.stringify([
    target.root_id.trim(),
    target.path.trim(),
    target.presentation,
    target.worker_id?.trim() ?? "",
    target.document_id?.trim() ?? "",
    target.document_revision ?? 0,
    target.version_id?.trim() ?? "",
  ]);
}
