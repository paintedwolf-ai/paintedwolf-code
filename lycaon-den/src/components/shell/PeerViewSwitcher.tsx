import { For, Show, createEffect, createSignal, onCleanup } from "solid-js";
import type { EditorWindow } from "../../platform/windows/editor-windows.ts";
import {
  createOverlayScopeFocusTrap,
  shellChromeInertTargets,
} from "../../platform/interaction/modal-focus-trap.ts";

export type PeerViewSwitcherProps = {
  views: readonly EditorWindow[];
  onFocus: (view: EditorWindow) => void;
  onClose: (view: EditorWindow) => void;
  onOpenChange?: (open: boolean) => void;
};

export type PeerViewSwitcherHandle = {
  open: () => void;
  isOpen: () => boolean;
  cancel: () => void;
};

/** Closing the main window only hides the workspace, so it is focus-only. */
const closable = (view: EditorWindow) =>
  view.nativeLabel !== null && view.nativeLabel !== "main";

/** Switches among peer views for the focused subject. */
export function PeerViewSwitcher(
  props: PeerViewSwitcherProps & {
    ref?: (handle: PeerViewSwitcherHandle) => void;
  },
) {
  const [open, setOpen] = createSignal(false);
  const [index, setIndex] = createSignal(0);
  let dialogEl: HTMLDivElement | undefined;

  const close = () => {
    setOpen(false);
    setIndex(0);
    props.onOpenChange?.(false);
  };

  const rows = () => props.views;

  props.ref?.({
    open: () => {
      if (rows().length === 0) return;
      setIndex(0);
      setOpen(true);
      props.onOpenChange?.(true);
    },
    isOpen: open,
    cancel: close,
  });

  createOverlayScopeFocusTrap(open, () => dialogEl, {
    autoFocus: false,
    inertTarget: () => shellChromeInertTargets(),
  });

  createEffect(() => {
    if (!open()) return;
    // Blur cancels the open switcher.
    window.addEventListener("blur", close);
    queueMicrotask(() => dialogEl?.focus());
    onCleanup(() => {
      window.removeEventListener("blur", close);
    });
  });

  const onDialogKeyDown = (e: KeyboardEvent) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const count = rows().length;
      if (count === 0) return;
      setIndex((i) => {
        if (e.key === "ArrowDown") return (i + 1) % count;
        return (i - 1 + count) % count;
      });
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      const view = rows()[index()];
      if (!view) return;
      close();
      props.onFocus(view);
      return;
    }
    if (e.key === "Backspace" || (e.key === "Delete" && !e.metaKey)) {
      e.preventDefault();
      const view = rows()[index()];
      if (!view || !closable(view)) return;
      props.onClose(view);
      if (rows().length <= 1) close();
      else setIndex((i) => Math.min(i, Math.max(0, rows().length - 2)));
    }
  };

  return (
    <Show when={open() && rows().length > 0}>
      <div
        class="recent-switcher-backdrop"
        data-testid="peer-view-switcher-backdrop"
      >
        <div
          ref={(el) => {
            dialogEl = el;
          }}
          class="recent-switcher"
          role="dialog"
          aria-modal="true"
          aria-label="Switch view"
          data-testid="peer-view-switcher"
          tabIndex={-1}
          onKeyDown={onDialogKeyDown}
        >
          <ul class="recent-switcher__list">
            <For each={rows()}>
              {(view, i) => (
                <li class="peer-view-switcher__item">
                  <button
                    type="button"
                    class="recent-switcher__row"
                    classList={{
                      "recent-switcher__row--active": i() === index(),
                    }}
                    data-testid="peer-view-switcher-row"
                    data-active={i() === index() ? "true" : undefined}
                    data-view-label={view.nativeLabel ?? undefined}
                    aria-current={i() === index() ? "true" : undefined}
                    onMouseEnter={() => setIndex(i())}
                    onClick={() => {
                      setIndex(i());
                      close();
                      props.onFocus(view);
                    }}
                  >
                    <span class="recent-switcher__primary">
                      {view.label}
                    </span>
                    <span class="recent-switcher__project">{view.title}</span>
                  </button>
                  <Show when={closable(view)}>
                    <button
                      type="button"
                      class="peer-view-switcher__close"
                      data-testid="peer-view-switcher-close"
                      aria-label={`Close ${view.label.toLowerCase()}`}
                      onClick={(e) => {
                        e.stopPropagation();
                        props.onClose(view);
                        if (rows().length <= 1) close();
                      }}
                    >
                      Close
                    </button>
                  </Show>
                </li>
              )}
            </For>
          </ul>
          <p class="recent-switcher__hint">Enter to focus · Delete to close</p>
        </div>
      </div>
    </Show>
  );
}
