import { Show, createEffect, createSignal, on } from "solid-js";
import {
  appMenuOpenRequests,
  inWindowAppMenuItems,
} from "../../platform/desktop/in-window-app-menu.ts";
import type { WindowChromeSide } from "../../platform/windows/window-chrome-layout.ts";
import { ContextMenu, type ContextMenuItem } from "../ContextMenu.tsx";
import { bindChromeContextMenu } from "../context-menu-open.ts";

/**
 * The window's app menu where the desktop has no menu bar. Every section the
 * native bar would carry opens from this one button, and from F10.
 */
export function AppMenuButton(props: { side: WindowChromeSide }) {
  const [items, setItems] = createSignal<ContextMenuItem[] | null>(null);
  let trigger: HTMLButtonElement | undefined;
  const close = () => setItems(null);
  const open = () => {
    trigger?.focus({ preventScroll: true });
    // The spec is read as the menu opens, so enablement is current.
    setItems(inWindowAppMenuItems());
  };
  const toggle = () => (items() ? close() : open());
  const binding = bindChromeContextMenu(open);
  const menu = () => {
    const entries = items();
    return trigger && entries ? { anchor: trigger, entries } : null;
  };
  createEffect(on(appMenuOpenRequests, toggle, { defer: true }));

  return (
    <>
      <button
        type="button"
        class="den-shell-window-control den-shell-app-menu-trigger"
        tabindex="-1"
        aria-label="App menu"
        aria-haspopup="menu"
        aria-expanded={items() !== null}
        data-testid="window-app-menu"
        ref={(el) => {
          trigger = el;
        }}
        onClick={toggle}
        onKeyDown={binding.onKeyDown}
        onContextMenu={binding.onContextMenu}
      >
        <svg viewBox="0 0 10 10" width="10" height="10" aria-hidden="true" class="den-shell-window-control__glyph">
          <path d="M1 2.5h8M1 5h8M1 7.5h8" />
        </svg>
      </button>
      <Show when={menu()} keyed>
        {(menu) => (
          <ContextMenu
            anchor={menu.anchor}
            items={menu.entries}
            label="App menu"
            align={props.side === "left" ? "start" : "end"}
            gap={6}
            onDismiss={close}
          />
        )}
      </Show>
    </>
  );
}
