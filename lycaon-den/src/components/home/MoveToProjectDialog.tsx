import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { Show, createSignal } from "solid-js";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenCheckboxControl } from "../primitives/DenCheckbox.tsx";
import {
  createModalFocusTrap,
  shellChromeInertTargets,
} from "../../platform/interaction/modal-focus-trap.ts";

type Props = {
  open: boolean;
  busy?: boolean;
  error?: string | null;
  onPickFolder: () => Promise<string | null>;
  onSubmit: (args: { rootPath: string; initGit: boolean }) => void;
  onClose: () => void;
};

/** Moves a draft's scratch work into a chosen empty folder and saves the project. */
export function MoveToProjectDialog(props: Props) {
  let dialogEl: HTMLDivElement | undefined;
  const [folder, setFolder] = createSignal("");
  const [initGit, setInitGit] = createSignal(true);

  const pick = async () => {
    const picked = await props.onPickFolder();
    if (picked) setFolder(picked);
  };

  createModalFocusTrap(
    () => props.open,
    () => dialogEl,
    {
      onEscape: () => {
        if (!props.busy) props.onClose();
      },
      inertTarget: () => shellChromeInertTargets(),
    },
  );

  return (
    <Show when={props.open}>
      <div
        class="den-dialog-backdrop"
        onClick={(e) => {
          if (e.target === e.currentTarget && !props.busy) props.onClose();
        }}
      >
        <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
        <div ref={dialogEl} class="den-dialog" role="dialog" aria-modal="true" aria-labelledby="move-project-title" data-testid="move-project-dialog">
          <header class="den-dialog__header" {...chromeProps()}>
            <h2 id="move-project-title">Move this chat into a project</h2>
          </header>
          <p class="den-dialog__hint">
            Pick an empty folder to keep your work. Everything from this chat moves in,
            and the project tracks that folder from now on.
          </p>

          <label class="den-dialog__label">Destination folder</label>
          <div class="den-dialog__pick">
            <span
              class="den-dialog__pick-path"
              classList={{ "den-dialog__pick-path--empty": !folder() }}
              data-testid="move-project-folder"
            >
              {folder() || "No folder chosen"}
            </span>
            <DenButton
              variant="secondary"
              compact
              data-testid="move-project-choose"
              onClick={() => void pick()}
              disabled={props.busy}
            >
              Choose…
            </DenButton>
          </div>

          <label class="den-dialog__check">
            <DenCheckboxControl
              checked={initGit()}
              onChange={(e) => setInitGit(e.currentTarget.checked)}
              data-testid="move-project-init-git"
            />
            Initialise git in the folder
          </label>

          <Show when={props.error}>
            <p class="den-dialog__error" data-testid="move-project-error">{props.error}</p>
          </Show>

          <footer class="den-dialog__footer">
            <DenButton variant="ghost" onClick={() => props.onClose()} disabled={props.busy}>
              Cancel
            </DenButton>
            <DenButton
              variant="primary"
              data-testid="move-project-submit"
              disabled={!folder().trim() || props.busy}
              onClick={() => props.onSubmit({ rootPath: folder().trim(), initGit: initGit() })}
            >
              {props.busy ? "Moving…" : "Move & keep project"}
            </DenButton>
          </footer>
        </div>
      </div>
    </Show>
  );
}
