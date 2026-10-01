import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import type { ProjectRoot, RewindPreviewResponse } from "../../api/types.ts";
import { rewindIssueCopy } from "../../chat/recovery/rewind-preview.ts";
import { For, Show } from "solid-js";
import { createModalFocusTrap } from "../../platform/interaction/modal-focus-trap.ts";
import type { RecoveryAction, RecoveryTarget } from "../../chat/recovery/session-recovery.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";

/** Recovery copy keeps conversation and file rollback atomic. */
const COPY: Record<RecoveryAction, { title: string; body: string; confirm: string; busy: string }> = {
  edit: {
    title: "Edit this ask?",
    body: "Removes this ask and everything after, reverses their file changes, and puts the ask in the composer.",
    confirm: "Edit",
    busy: "Working…",
  },
  rewind: {
    title: "Rewind from here?",
    body: "Removes this ask and everything after, reverses their file changes, and puts the ask in the composer.",
    confirm: "Rewind",
    busy: "Working…",
  },
};

type Props = {
  target: RecoveryTarget | null;
  busy?: boolean;
  preview?: RewindPreviewResponse | null;
  roots?: readonly ProjectRoot[];
  previewLoading?: boolean;
  /** Recovery waits for the host to become idle. */
  stopsLive?: boolean;
  error?: string | null;
  onRefresh?: () => void;
  onConfirm: () => void;
  onCancel: () => void;
};

export function SessionRecoverDialog(props: Props) {
  const displayPath = (rootId: string, path: string): string => {
    if ((props.roots?.length ?? 0) < 2) return path;
    const root = props.roots?.find((entry) => entry.id === rootId);
    const label = root?.label || root?.path.split("/").filter(Boolean).at(-1);
    return label ? `${label}: ${path}` : path;
  };
  let dialogEl: HTMLDivElement | undefined;
  // The shared trap controls focus, Escape, and focus return.
  createModalFocusTrap(() => props.target != null, () => dialogEl, {
    onEscape: () => {
      if (!props.busy) props.onCancel();
    },
  });
  return (
    <Show when={props.target} keyed>
      {(target) => {
        const copy = COPY[target.action];
        return (
          <div
            class="den-dialog-backdrop"
            onClick={(e) => {
              if (e.target === e.currentTarget && !props.busy) props.onCancel();
            }}
          >
            <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
            <div
              ref={dialogEl}
              class="den-dialog den-rewind-dialog"
              role="dialog"
              aria-modal="true"
              aria-labelledby="session-recover-title"
              data-testid="session-recover-dialog"
            >
              <header class="den-dialog__header" {...chromeProps()}>
                <h2 id="session-recover-title">{copy.title}</h2>
              </header>
              <Scrollport class="den-dialog__body">
              <p class="den-dialog__hint">
                {copy.body}
                <Show when={props.stopsLive}>
                  {" "}Wait for the current response to finish.
                </Show>
              </p>
              <Show when={props.previewLoading}>
                <p class="den-dialog__hint" role="status">Checking file history…</p>
              </Show>
              <Show when={props.preview}>
                {(preview) => <div class="den-dialog__hint">
                  <Show when={preview().issues.length === 0}>
                    <p>{preview().files.length === 0 ? "No files will change." : `${preview().files.length} ${preview().files.length === 1 ? "file will" : "files will"} change.`}</p>
                  </Show>
                  <ul><For each={preview().files}>{(file) => <li>{displayPath(file.root_id, file.path)}{file.path !== file.target_path ? ` → ${file.target_path}` : ""}</li>}</For></ul>
                  <Show when={preview().issues.length > 0}>
                    <p role="alert">Rewind is unavailable for this selection:</p>
                    <ul><For each={preview().issues}>{(issue) => <li><Show when={issue.path}><strong>{displayPath(issue.root_id, issue.path)}</strong>: </Show>{rewindIssueCopy(issue.code)}</li>}</For></ul>
                  </Show>
                </div>}
              </Show>
              <Show when={props.error}>
                {(message) => (
                  <p class="den-dialog__hint" data-testid="session-recover-error" role="alert">
                    {message()}
                  </p>
                )}
              </Show>
              </Scrollport>
              <footer class="den-dialog__footer">
                <Show when={props.onRefresh}>
                  <DenButton variant="ghost" disabled={props.busy || props.previewLoading} onClick={() => props.onRefresh?.()}>
                    Refresh preview
                  </DenButton>
                </Show>
                <DenButton
                  variant="ghost"
                  data-testid="session-recover-cancel"
                  disabled={props.busy}
                  onClick={() => props.onCancel()}
                >
                  Cancel
                </DenButton>
                <DenButton
                  variant="primary"
                  data-testid="session-recover-confirm"
                  disabled={props.busy || props.stopsLive || props.previewLoading || !props.preview?.plan_digest || props.preview.issues.length > 0}
                  onClick={() => props.onConfirm()}
                >
                  {props.busy ? copy.busy : copy.confirm}
                </DenButton>
              </footer>
            </div>
          </div>
        );
      }}
    </Show>
  );
}
