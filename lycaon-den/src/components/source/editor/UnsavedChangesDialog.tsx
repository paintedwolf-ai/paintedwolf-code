import { ChromeDragSurface } from "../../shell/ChromeDragSurface.tsx";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { For, Show } from "solid-js";
import { ResidentPortal } from "../../primitives/ResidentPortal.tsx";
import { createModalFocusTrap } from "../../../platform/interaction/modal-focus-trap.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { Scrollport } from "../../primitives/Scrollport.tsx";
import { multiFileDirtyDialogBody } from "../../../files/tabs/files-tab-strip.ts";

/** What the user was doing when the dirty guard interrupted them. */
export type UnsavedChangesIntent =
  | "close"
  | "discard"
  | "reload"
  | "bulk-close"
  | "editor-action";

const COPY: Record<
  UnsavedChangesIntent,
  { title: string; body: string; confirm: string }
> = {
  "bulk-close": {
    title: "Unsaved changes",
    // Bulk renders the file list, not this sentence.
    body: "These files have unsaved changes.",
    confirm: "Discard all",
  },
  close: {
    title: "Unsaved changes",
    body: "This file has unsaved changes. Save them before closing?",
    confirm: "Discard & close",
  },
  "editor-action": {
    title: "Unsaved changes",
    body: "Save or discard your edits before this editor action runs against the file on disk.",
    confirm: "Discard & continue",
  },
  discard: {
    title: "Discard changes?",
    body: "Return this file to its saved version?",
    confirm: "Discard changes",
  },
  reload: {
    title: "Reload from disk?",
    body: "The file reloads from disk and your unsaved edits will be lost.",
    confirm: "Discard & reload",
  },
};

type Props = {
  intent: UnsavedChangesIntent | null;
  /** Offer "Save changes" / "Save all" as the primary action. */
  canSave?: boolean;
  /** The draft carries an AI edit disk never received; history retains it. */
  heldAgentEdit?: boolean;
  saving?: boolean;
  /** Multi-file bulk-close: basenames to list (capped in the dialog). */
  fileNames?: string[];
  onCancel: () => void;
  onDiscard: () => void;
  onSave?: () => void;
};

/** Dirty-file guard for the stage editor. */
export function UnsavedChangesDialog(props: Props) {
  let dialogEl: HTMLDivElement | undefined;
  createModalFocusTrap(() => props.intent != null, () => dialogEl, {
    onEscape: () => {
      if (!props.saving) props.onCancel();
    },
  });
  return (
    <Show when={props.intent} keyed>
      {(intent) => {
        const bulk = intent === "bulk-close";
        const names = () => props.fileNames ?? [];
        const body = () => multiFileDirtyDialogBody(names());
        const copy = COPY[intent];
        const offerSave =
          (intent === "close" || intent === "editor-action" || bulk) &&
          props.canSave === true;
        return (
          <ResidentPortal mount={document.body}>
            <div
              class="den-dialog-backdrop den-unsaved-dialog-backdrop"
              onClick={(e) => {
                if (e.target === e.currentTarget && !props.saving) props.onCancel();
              }}
            >
              <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
              <div
                ref={dialogEl}
                class="den-dialog"
                role="alertdialog"
                aria-modal="true"
                aria-labelledby="editor-unsaved-title"
                data-testid="editor-unsaved-dialog"
              >
                <header class="den-dialog__header" {...chromeProps()}>
                  <h2 id="editor-unsaved-title">
                    {copy.title}
                  </h2>
                </header>
                <Show
                  when={bulk}
                  fallback={
                    <>
                      <p class="den-dialog__hint">{copy.body}</p>
                      <Show when={props.heldAgentEdit}>
                        <p
                          class="den-dialog__hint"
                          data-testid="editor-unsaved-held-agent-edit"
                        >
                          The AI's edit is kept in version history, so you can
                          restore it from the version picker.
                        </p>
                      </Show>
                    </>
                  }
                >
                  <div class="den-dialog__hint" data-testid="editor-unsaved-bulk-body">
                    <p>These files have unsaved changes:</p>
                    <Scrollport
                      class="den-unsaved-dialog__files"
                      contentAs="ul"
                      contentClass="den-unsaved-dialog__files-content"
                      axis="both"
                    >
                      <For each={body().listed}>
                        {(name) => <li>{name}</li>}
                      </For>
                    </Scrollport>
                    <Show when={body().andMore > 0}>
                      <p data-testid="editor-unsaved-and-more">
                        and {body().andMore} more
                      </p>
                    </Show>
                  </div>
                </Show>
                <footer class="den-dialog__footer">
                  <DenButton
                    variant="ghost"
                    data-testid="editor-unsaved-cancel"
                    disabled={props.saving}
                    onClick={() => props.onCancel()}
                  >
                    {intent === "close" ? "Keep editing" : "Cancel"}
                  </DenButton>
                  <DenButton
                    variant="danger"
                    data-testid="editor-unsaved-discard"
                    disabled={props.saving}
                    onClick={() => props.onDiscard()}
                  >
                    {copy.confirm}
                  </DenButton>
                  <Show when={offerSave}>
                    <DenButton
                      variant="primary"
                      data-testid="editor-unsaved-save"
                      disabled={props.saving}
                      onClick={() => props.onSave?.()}
                    >
                      {props.saving
                        ? "Saving…"
                        : bulk
                          ? "Save all"
                          : "Save changes"}
                    </DenButton>
                  </Show>
                </footer>
              </div>
            </div>
          </ResidentPortal>
        );
      }}
    </Show>
  );
}
