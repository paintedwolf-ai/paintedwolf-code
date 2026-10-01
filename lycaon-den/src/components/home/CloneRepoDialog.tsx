import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { Show, createSignal } from "solid-js";
import { createModalFocusTrap } from "../../platform/interaction/modal-focus-trap.ts";
import { EXTENSION_SUGGESTIONS_COPY } from "../../settings/extensions/extension-suggestions-copy.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenInput } from "../primitives/DenInput.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";

export type CloneRepoSubmit = {
  url: string;
  parentDir: string;
};

type Props = {
  open: boolean;
  busy?: boolean;
  error?: string | null;
  onPickParent: () => Promise<string | null>;
  onSubmit: (args: CloneRepoSubmit) => void;
  onClose: () => void;
};

/** Clones a remote into a chosen folder and opens it as a project. */
export function CloneRepoDialog(props: Props) {
  let dialogEl: HTMLDivElement | undefined;
  const [url, setUrl] = createSignal("");
  const [parent, setParent] = createSignal("");

  createModalFocusTrap(
    () => props.open,
    () => dialogEl,
    {
      onEscape: () => {
        if (!props.busy) props.onClose();
      },
    },
  );

  const pick = async () => {
    const picked = await props.onPickParent();
    if (picked) setParent(picked);
  };

  const canSubmit = () => !!url().trim() && !!parent().trim() && !props.busy;

  return (
    <Show when={props.open}>
      <div
        class="den-dialog-backdrop"
        onClick={(e) => {
          if (e.target === e.currentTarget && !props.busy) props.onClose();
        }}
      >
        <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
        <div
          ref={dialogEl}
          class="den-dialog"
          role="dialog"
          aria-modal="true"
          aria-labelledby="clone-repo-title"
          data-testid="clone-repo-dialog"
        >
          <header class="den-dialog__header" {...chromeProps()}>
            <h2 id="clone-repo-title">Clone a repo</h2>
          </header>

          <Scrollport class="den-dialog__body">
            <label class="den-dialog__label" for="clone-repo-url">Repository URL</label>
            <DenInput
              id="clone-repo-url"
              class="den-dialog__field"
              data-testid="clone-repo-url"
              value={url()}
              placeholder="https://github.com/owner/repo.git"
              onInput={(e) => setUrl(e.currentTarget.value)}
            />

            <label class="den-dialog__label">Clone into</label>
            <div class="den-dialog__pick">
              <span
                class="den-dialog__pick-path"
                classList={{ "den-dialog__pick-path--empty": !parent() }}
                data-testid="clone-repo-parent"
              >
                {parent() || "No folder chosen"}
              </span>
              <DenButton
                variant="secondary"
                compact
                onClick={() => void pick()}
                disabled={props.busy}
              >
                Choose…
              </DenButton>
            </div>

            <Show when={props.error}>
              <p class="den-dialog__error" data-testid="clone-repo-error">{props.error}</p>
            </Show>
          </Scrollport>

          <footer class="den-dialog__footer">
            <DenButton
              variant="ghost"
              class="den-dialog__footer-start"
              onClick={() => props.onClose()}
              disabled={props.busy}
            >
              Cancel
            </DenButton>
            <div class="den-dialog__footer-end">
              <DenButton
                variant="primary"
                data-testid="clone-repo-submit"
                disabled={!canSubmit()}
                onClick={() =>
                  props.onSubmit({
                    url: url().trim(),
                    parentDir: parent().trim(),
                  })
                }
              >
                {props.busy ? "Cloning…" : EXTENSION_SUGGESTIONS_COPY.cloneAction}
              </DenButton>
            </div>
          </footer>
        </div>
      </div>
    </Show>
  );
}
