import { Show, createSignal, onCleanup, onMount } from "solid-js";
import { setHostFolderPresenter } from "../platform/files/host-folder-dialog.ts";
import { createModalFocusTrap } from "../platform/interaction/modal-focus-trap.ts";
import { ResidentPortal } from "./primitives/ResidentPortal.tsx";
import { DenInput } from "./primitives/DenInput.tsx";
import { DenButton } from "./primitives/DenButton.tsx";
import { Scrollport } from "./primitives/Scrollport.tsx";
import { ChromeDragSurface } from "./shell/ChromeDragSurface.tsx";
import { chromeProps } from "../styling/ui-chrome.ts";

export function HostFolderDialog() {
  const [open, setOpen] = createSignal(false);
  const [path, setPath] = createSignal("");
  const pending: Array<(path: string | null) => void> = [];
  let dialog: HTMLFormElement | undefined;

  const settle = (value: string | null) => {
    const resolve = pending.shift();
    setPath("");
    setOpen(pending.length > 0);
    resolve?.(value);
  };
  createModalFocusTrap(open, () => dialog, { onEscape: () => settle(null) });

  onMount(() => {
    setHostFolderPresenter(() => new Promise<string | null>((resolve) => {
      pending.push(resolve);
      setOpen(true);
    }));
  });
  onCleanup(() => {
    setHostFolderPresenter(null);
    for (const resolve of pending.splice(0)) resolve(null);
  });

  return (
    <Show when={open()}>
      <ResidentPortal mount={document.body}>
        <div
          class="den-dialog-backdrop den-dialog-backdrop--viewport"
          onClick={(event) => { if (event.target === event.currentTarget) settle(null); }}
        >
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <form
            ref={dialog}
            class="den-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="host-folder-title"
            data-testid="host-folder-dialog"
            onSubmit={(event) => {
              event.preventDefault();
              if (path().trim()) settle(path().trim());
            }}
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="host-folder-title">Choose a folder</h2>
            </header>
            <Scrollport class="den-dialog__body">
              <label for="host-folder-path">Folder path</label>
              <DenInput
                id="host-folder-path"
                autofocus
                autocomplete="off"
                spellcheck={false}
                value={path()}
                onInput={(event) => setPath(event.currentTarget.value)}
                aria-describedby="host-folder-hint"
              />
              <p id="host-folder-hint" class="den-dialog__hint">
                Enter the absolute path to a folder on the connected host.
              </p>
            </Scrollport>
            <footer class="den-dialog__footer">
              <DenButton type="button" variant="ghost" onClick={() => settle(null)}>Cancel</DenButton>
              <DenButton type="submit" variant="primary" disabled={!path().trim()}>Choose folder</DenButton>
            </footer>
          </form>
        </div>
      </ResidentPortal>
    </Show>
  );
}
