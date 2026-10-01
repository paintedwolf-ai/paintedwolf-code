import type { ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { resolveProjectFile, relativeUnderRoot, type ResolveProjectRoot } from "../../api/project-path.ts";
import { fileVersionFromSnapshot } from "../../files/history/file-version.ts";
import { openFilesBuffer } from "../../files/documents/project-files-buffers.ts";
import { previewDiffBufferJobId } from "../../files/review/review-model.ts";
import { openReviewLens } from "../../files/review/review-pane.ts";
import { diffsPageCopy, type DiffsAddress } from "../../files/review/diffs-address.ts";
import { resolvedLensView } from "../../files/tree/scope-resolution.ts";

type OpenFilesStageRequest = {
  kind: "stage";
  projectId: string;
};

type OpenFilesChangePreviewRequest = {
  kind: "file-change-preview";
  projectId: string;
  rootId: string;
  path: string;
  previewId?: string;
  title?: string;
  detail?: string;
  before: string | null;
  after: string | null;
};

type OpenFilesTrustReviewRequest = { kind: "trust-review"; projectId: string };

type OpenFilesDiffsRequest = {
  kind: "diffs";
  projectId: string;
  address: DiffsAddress;
};

type OpenFilesSurfaceRequest =
  | { kind: "chat-content"; projectId: string; document: ChatContentDocument }
  | OpenFilesTrustReviewRequest
  | OpenFilesDiffsRequest
  | OpenFilesStageRequest
  | OpenFilesChangePreviewRequest;

type OpenFilesSurfaceSink = (req: OpenFilesSurfaceRequest) => void;

let sink: OpenFilesSurfaceSink | null = null;
let pending: OpenFilesSurfaceRequest | null = null;

export function registerOpenFilesSurfaceSink(
  next: OpenFilesSurfaceSink,
): () => void {
  sink = next;
  if (pending) {
    next(pending);
    pending = null;
  }
  return () => {
    if (sink === next) sink = null;
  };
}

export function openFilesSurface(req: OpenFilesSurfaceRequest): void {
  if (sink) sink(req);
  else pending = req;
}

export function applyOpenFilesSurfaceRequest(
  req: OpenFilesSurfaceRequest,
  roots: readonly ResolveProjectRoot[],
): void {
  const projectId = req.projectId.trim();
  if (!projectId || req.kind === "stage") return;

  if (req.kind === "chat-content") {
    openFilesBuffer(projectId, { kind: "chat", rootId: "", rootLabel: "", path: "",
      chatContent: req.document, name: req.document.title, intent: "permanent" });
    return;
  }
  if (req.kind === "diffs") {
    // The comparison names the page, so duplicates stay distinct.
    const name = diffsPageCopy(req.address, resolvedLensView(projectId)).tab;
    openFilesBuffer(projectId, { kind: "diffs", rootId: "", rootLabel: "", path: "",
      diffs: req.address, name, intent: "transient" });
    return;
  }
  if (req.kind === "trust-review") {
    openFilesBuffer(projectId, { kind: "trust", rootId: "", rootLabel: "", path: "",
      name: "Trust changes", intent: "permanent" });
    return;
  }
  const resolved = resolveProjectFile(
    { roots: req.rootId ? roots.filter((root) => root.id === req.rootId) : roots },
    req.path,
  );
  const rootId = "error" in resolved ? req.rootId : resolved.rootId;
  const root = roots.find((candidate) => candidate.id === rootId);
  const rootLabel = root?.label ?? root?.path.split(/[\\/]/).filter(Boolean).pop() ?? "";
  const path = root && !("error" in resolved)
    ? relativeUnderRoot(resolved.absolutePath, root.path) ?? req.path
    : req.path;
  openReviewLens(projectId, { kind: "new" });
  openFilesBuffer(projectId, {
    intent: "transient",
    rootId,
    rootLabel,
    path,
    kind: "diff",
    jobId: previewDiffBufferJobId(req.previewId ?? req.path),
    diffPreview: {
      version: fileVersionFromSnapshot({ rootId, path, before: req.before, after: req.after }),
      ...(req.title ? { title: req.title, detail: req.detail } : {}),
    },
  });
}

export function resetOpenFilesSurfaceForTests(): void {
  sink = null;
  pending = null;
}
