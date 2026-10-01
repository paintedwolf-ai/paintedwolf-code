import { For, Show } from "solid-js";
import type { ApprovalFileChange } from "../../api/types.ts";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { wholeFileOpChange } from "../source/reader/source-reader-change.ts";

export function ApprovalFileChanges(props: {
  changes?: ApprovalFileChange[];
  projectId?: string;
  checkpointId: string;
}) {
  const openChange = (change: ApprovalFileChange) => {
    if (!props.projectId) return;
    const detail = [
      change.from_path ? `${change.from_path} → ${change.path}` : change.path,
      change.target === "index" ? "Git index" : change.operation,
      change.preview_note,
      `${change.before_bytes} → ${change.after_bytes} bytes`,
      change.preview_note && change.before_sha256 ? `Before SHA-256: ${change.before_sha256}` : undefined,
      change.preview_note && change.after_sha256 ? `After SHA-256: ${change.after_sha256}` : undefined,
    ].filter(Boolean).join(" · ");
    openFilesSurface({
      kind: "file-change-preview",
      projectId: props.projectId,
      rootId: change.root_id ?? "",
      path: change.path,
      previewId: `${props.checkpointId}:${change.root_id ?? ""}:${change.target ?? "file"}:${change.path}`,
      before: change.operation === "create" ? null : change.preview_note ? "" : change.before,
      after: change.operation === "delete" ? null : change.preview_note ? "" : change.after,
      title: change.target === "index" ? "Proposed staged change" : "Proposed file change",
      detail,
    });
  };
  return (
    <Show when={props.projectId && props.changes?.length}>
      <div class="min-w-0" data-testid="approval-file-changes">
        <For each={props.changes}>{(change) => (
          <DenButton variant="ghost" compact class="max-w-full whitespace-normal break-all text-left" onClick={() => openChange(change)}>
            {APPROVALS_COPY.card.viewDiff} · <span data-file-change={change.target === "index" ? undefined : wholeFileOpChange(change.operation)}>{change.path}</span>{change.target === "index" ? " (Git index)" : ""}
          </DenButton>
        )}</For>
      </div>
    </Show>
  );
}
