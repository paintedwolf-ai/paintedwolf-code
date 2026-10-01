import { ChromeDragSurface } from "./shell/ChromeDragSurface.tsx";
import { For, Show, createSignal, onCleanup, onMount } from "solid-js";
import { ResidentPortal } from "./primitives/ResidentPortal.tsx";
import {
  setConfirmDestructivePresenter,
  type ConfirmDestructiveRequest,
} from "../platform/interaction/confirm-dialog.ts";
import { createModalFocusTrap } from "../platform/interaction/modal-focus-trap.ts";
import { chromeProps } from "../styling/ui-chrome.ts";
import { DenButton } from "./primitives/DenButton.tsx";

type DialogProps = {
  request: ConfirmDestructiveRequest | null;
  onCancel: () => void;
  onConfirm: () => void;
};

function messageLines(message: string): string[] {
  return message.split(/\n+/).map((line) => line.trim()).filter(Boolean);
}

export function ConfirmDestructiveDialog(props: DialogProps) {
  let dialogEl: HTMLDivElement | undefined;
  createModalFocusTrap(() => props.request != null, () => dialogEl, {
    onEscape: () => props.onCancel(),
  });
  return (
    <Show when={props.request} keyed>
      {(req) => (
        <ResidentPortal mount={document.body}>
          <div
            class="den-dialog-backdrop den-dialog-backdrop--viewport"
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
              aria-labelledby="confirm-destructive-title"
              data-testid="confirm-destructive-dialog"
            >
              <header class="den-dialog__header" {...chromeProps()}>
                <h2 id="confirm-destructive-title">{req.title}</h2>
              </header>
              <For each={messageLines(req.message)}>
                {(line) => <p class="den-dialog__hint">{line}</p>}
              </For>
              <footer class="den-dialog__footer">
                <DenButton
                  variant="ghost"
                  data-testid="confirm-destructive-cancel"
                  autofocus
                  onClick={() => props.onCancel()}
                >
                  {req.cancelLabel ?? "Cancel"}
                </DenButton>
                <DenButton
                  variant={req.destructive === false ? "primary" : "danger"}
                  data-testid="confirm-destructive-ok"
                  onClick={() => props.onConfirm()}
                >
                  {req.okLabel}
                </DenButton>
              </footer>
            </div>
          </div>
        </ResidentPortal>
      )}
    </Show>
  );
}

type Queued = {
  request: ConfirmDestructiveRequest;
  resolve: (ok: boolean) => void;
};

/** Registers the in-app presenter for `confirmDestructive`. */
export function ConfirmDestructiveHost() {
  const [active, setActive] = createSignal<Queued | null>(null);
  const waiting: Queued[] = [];
  let occupied = false;

  const settle = (ok: boolean) => {
    const current = active();
    if (!current) return;
    current.resolve(ok);
    const next = waiting.shift();
    if (next) {
      setActive(next);
      return;
    }
    occupied = false;
    setActive(null);
  };

  onMount(() => {
    setConfirmDestructivePresenter((request) => {
      return new Promise<boolean>((resolve) => {
        const item: Queued = { request, resolve };
        if (occupied) {
          waiting.push(item);
          return;
        }
        occupied = true;
        // Let the opening click (context menu dismiss) finish first.
        queueMicrotask(() => setActive(item));
      });
    });
    onCleanup(() => {
      setConfirmDestructivePresenter(null);
      const current = active();
      if (current) current.resolve(false);
      while (waiting.length > 0) waiting.shift()?.resolve(false);
      occupied = false;
      setActive(null);
    });
  });

  return (
    <ConfirmDestructiveDialog
      request={active()?.request ?? null}
      onCancel={() => settle(false)}
      onConfirm={() => settle(true)}
    />
  );
}
