import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { Show, createEffect, createSignal, onCleanup } from "solid-js";
import type { FolderDetect } from "../../api/types.ts";
import { createModalFocusTrap } from "../../platform/interaction/modal-focus-trap.ts";
import { EXTENSION_SUGGESTIONS_COPY } from "../../settings/extensions/extension-suggestions-copy.ts";
import { hasInstallableExtensionSuggestions } from "../../settings/extensions/extension-suggestions-model.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { ExtensionSuggestionsBlock } from "./ExtensionSuggestionsBlock.tsx";

export type OpenFolderChoice = {
  installIds: string[];
};

type Props = {
  open: boolean;
  path: string | null;
  detect: FolderDetect | null;
  busy?: boolean;
  onConfirm: (choice: OpenFolderChoice) => void;
  onClose: () => void;
};

/** Delay before backdrop dismissal is enabled. */
export const OPEN_FOLDER_BACKDROP_ARM_MS = 350;

export function OpenFolderConfirm(props: Props) {
  let dialogEl: HTMLDivElement | undefined;
  const [installIds, setInstallIds] = createSignal<string[]>([]);
  const [backdropArmed, setBackdropArmed] = createSignal(false);
  const titlePath = () => props.detect?.path ?? props.path ?? "";
  const suggestions = () => props.detect?.extension_suggestions ?? null;
  const hasInstalls = () => hasInstallableExtensionSuggestions(suggestions());

  createEffect(() => {
    if (!props.open || props.path == null) {
      setBackdropArmed(false);
      return;
    }
    setBackdropArmed(false);
    const timer = window.setTimeout(() => setBackdropArmed(true), OPEN_FOLDER_BACKDROP_ARM_MS);
    onCleanup(() => window.clearTimeout(timer));
  });

  createModalFocusTrap(
    () => props.open && props.path != null,
    () => dialogEl,
    {
      onEscape: () => {
        if (!props.busy) props.onClose();
      },
    },
  );

  const primaryLabel = () => {
    if (props.busy) return "Opening…";
    const n = installIds().length;
    return n > 0
      ? EXTENSION_SUGGESTIONS_COPY.installAndOpen(n, "open")
      : EXTENSION_SUGGESTIONS_COPY.openAction;
  };

  const dismissFromBackdrop = (e: MouseEvent) => {
    if (e.target !== e.currentTarget) return;
    // Ignore the click that opened the dialog.
    if (props.busy || !backdropArmed()) return;
    props.onClose();
  };

  return (
    <Show when={props.open && props.path ? props.path : null} keyed>
      {(path) => (
        <div class="den-dialog-backdrop" data-testid="open-folder-backdrop" onClick={dismissFromBackdrop}>
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <div
            ref={dialogEl}
            class="den-dialog"
            classList={{ "den-dialog--wide": hasInstalls() }}
            role="dialog"
            aria-modal="true"
            aria-labelledby="open-folder-title"
            data-testid="open-folder-confirm"
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="open-folder-title">
                {EXTENSION_SUGGESTIONS_COPY.dialogTitle(folderName(titlePath()))}
              </h2>
            </header>
            <Scrollport class="den-dialog__body">
              <p class="den-dialog__hint" data-testid="open-folder-path">
                {path}
              </p>

              <Show when={suggestions()} keyed>
                {(value) => (
                  <ExtensionSuggestionsBlock
                    response={value}
                    onSelectionChange={setInstallIds}
                  />
                )}
              </Show>
            </Scrollport>

            <footer class="den-dialog__footer">
              <div class="den-dialog__footer-end">
                <DenButton
                  variant="ghost"
                  data-testid="open-folder-cancel"
                  disabled={props.busy}
                  onClick={() => props.onClose()}
                >
                  {EXTENSION_SUGGESTIONS_COPY.cancel}
                </DenButton>
                <DenButton
                  variant="primary"
                  data-testid="open-folder-confirm-submit"
                  disabled={props.busy}
                  onClick={() => props.onConfirm({ installIds: installIds() })}
                >
                  {primaryLabel()}
                </DenButton>
              </div>
            </footer>
          </div>
        </div>
      )}
    </Show>
  );
}

function folderName(path: string): string {
  const parts = path.replace(/[/\\]+$/, "").split(/[/\\]/);
  return parts[parts.length - 1] || path;
}
