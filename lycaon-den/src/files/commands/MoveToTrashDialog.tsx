import { ChromeDragSurface } from "../../components/shell/ChromeDragSurface.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { Show } from "solid-js";
import { ResidentPortal } from "../../components/primitives/ResidentPortal.tsx";
import { createModalFocusTrap } from "../../platform/interaction/modal-focus-trap.ts";
import { OpenInButton } from "../../components/OpenInButton.tsx";
import type { LocalPathTarget } from "../../platform/navigation/open-local-path.ts";
import { hostSharesDevice } from "../../platform/connection/host-identity.ts";
import { DenButton } from "../../components/primitives/DenButton.tsx";

export type TrashConfirmState = {
  operationId: string;
  sessionId?: string;
  rootId: string;
  path: string;
  name: string;
  isDir: boolean;
  body: string;
  /** Host explanation and recovery actions after a failed attempt. */
  error?: string;
  retryable?: boolean;
  suggestedAction?: string;
};

type Props = {
  state: TrashConfirmState | null;
  busy?: boolean;
  onCancel: () => void;
  onConfirm: () => void;
  openIn?: LocalPathTarget | null;
};

/** Cancel receives initial focus to prevent accidental confirmation. */
export function MoveToTrashDialog(props: Props) {
  let dialogEl: HTMLDivElement | undefined;
  createModalFocusTrap(() => props.state != null, () => dialogEl, {
    onEscape: () => {
      props.onCancel();
    },
  });
  return (
    <Show when={props.state} keyed>
      {(st) => (
        <ResidentPortal mount={document.body}>
          <div
            class="den-dialog-backdrop"
            onClick={(e) => {
              if (e.target === e.currentTarget) props.onCancel();
            }}
          >
            <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
            <div
              ref={dialogEl}
              class="den-dialog"
              role="alertdialog"
              aria-modal="true"
              aria-labelledby="files-trash-title"
              aria-describedby="files-trash-body"
              aria-busy={props.busy}
              data-testid="files-trash-dialog"
            >
              <header class="den-dialog__header" {...chromeProps()}>
                <h2 id="files-trash-title">{st.error ? `Couldn’t move “${st.name}” to trash` : "Move to trash?"}</h2>
              </header>
              <p id="files-trash-body" class="den-dialog__hint" data-testid="files-trash-body">
                {st.error ?? st.body}
              </p>
              <Show when={st.error && st.suggestedAction}>
                <p class="den-dialog__hint">{st.suggestedAction}</p>
              </Show>
              <footer class="den-dialog__footer">
                <DenButton
                  variant="ghost"
                  data-testid="files-trash-cancel"
                  autofocus
                  onClick={() => props.onCancel()}
                >
                  {props.busy ? "Continue working" : st.error ? "Close" : "Cancel"}
                </DenButton>
                <Show when={st.error && hostSharesDevice()}>
                  <OpenInButton target={props.openIn ?? null} disabled={props.busy} />
                </Show>
                <Show when={!st.error || st.retryable === true}>
                  <DenButton
                    variant="danger"
                    data-testid="files-trash-confirm"
                    disabled={props.busy}
                    onClick={() => props.onConfirm()}
                  >
                    {props.busy ? "Moving…" : st.error ? "Retry" : "Move to trash"}
                  </DenButton>
                </Show>
              </footer>
            </div>
          </div>
        </ResidentPortal>
      )}
    </Show>
  );
}
