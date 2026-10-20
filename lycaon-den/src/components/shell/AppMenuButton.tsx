import { Show, createEffect, createSignal, on } from "solid-js";
import {
  appMenuOpenRequests,
  inWindowAppMenuItems,
} from "../../platform/desktop/in-window-app-menu.ts";
import type { WindowChromeSide } from "../../platform/windows/window-chrome-layout.ts";
import { ContextMenu, type ContextMenuItem } from "../ContextMenu.tsx";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { bindChromeContextMenu } from "../context-menu-open.ts";

/**
 * The window's app menu where the desktop has no menu bar. Every section the
 * native bar would carry opens from this one button, and from F10.
 */
export function AppMenuButton(props: { side: WindowChromeSide }) {
  const [items, setItems] = createSignal<ContextMenuItem[] | null>(null);
  const [fromKeyboard, setFromKeyboard] = createSignal(false);
  let trigger: HTMLButtonElement | undefined;
  const close = () => setItems(null);
  const open = (keyboard: boolean) => {
    const entries = inWindowAppMenuItems();
    trigger?.focus({ preventScroll: true });
    setFromKeyboard(keyboard);
    setItems(entries);
  };
  const toggle = (keyboard: boolean) => (items() ? close() : open(keyboard));
  const binding = bindChromeContextMenu(() => open(false));
  const menu = () => {
    const entries = items();
    return trigger && entries ? { anchor: trigger, entries, keyboard: fromKeyboard() } : null;
  };
  // F10 opens the menu like a menu bar, with its first entry current.
  createEffect(on(appMenuOpenRequests, () => toggle(true), { defer: true }));

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
        onPointerDown={(event) => event.preventDefault()}
        onClick={() => toggle(false)}
        onKeyDown={binding.onKeyDown}
        onContextMenu={binding.onContextMenu}
      >
        <ThemeIcon slot="app-menu" size={16} />
      </button>
      <Show when={menu()} keyed>
        {(menu) => (
          <ContextMenu
            anchor={menu.anchor}
            items={menu.entries}
            label="App menu"
            keyboard={menu.keyboard}
            align={props.side === "left" ? "start" : "end"}
            gap={6}
            onDismiss={close}
          />
        )}
      </Show>
    </>
  );
}
