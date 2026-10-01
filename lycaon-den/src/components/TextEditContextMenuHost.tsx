import { linkMenuItems } from "./link-menu-items.ts";
import { dispatchKeyboardContextMenu } from "./context-menu-open.ts";
import { Show, createSignal, onCleanup, onMount } from "solid-js";
import {
  ContextMenu,
  type ContextMenuAnchor,
  type ContextMenuItem,
} from "./ContextMenu.tsx";
import { textEditMenuItems } from "./text-edit-menu-items.ts";
import {
  findTextEditable,
  resolveTextEditContext,
} from "../platform/interaction/text-edit-context.ts";
import {
  setupContextMenuGuard,
  teardownContextMenuGuard,
} from "../platform/interaction/context-menu.ts";

type MenuState = {
  anchor: ContextMenuAnchor;
  items: ContextMenuItem[];
  retainFocus: boolean;
};

export function TextEditContextMenuHost() {
  const [menu, setMenu] = createSignal<MenuState | null>(null);

  onMount(() => {
    const open = (event: MouseEvent, items: ContextMenuItem[]) => {
      event.preventDefault();
      setMenu({
        anchor: { x: event.clientX, y: event.clientY },
        items,
        // Keyboard-invoked menus report button 0 and want real focus.
        retainFocus: event.button === 2,
      });
    };

    const onSelectionCapture = (event: MouseEvent) => {
      if (event.defaultPrevented) return;
      if (findTextEditable(event.target)) return;
      const link = event.target instanceof Element ? event.target.closest("a[href]") : null;
      const linkItems = link ? linkMenuItems(link.getAttribute("href") ?? "") : [];
      if (linkItems.length) {
        event.stopPropagation();
        open(event, linkItems);
        return;
      }
      // Source actions belong to the link even when its label is selected.
      if (event.target instanceof Element && event.target.closest(
        "[data-den-source-path], [data-den-navigation-reference]",
      )) return;
      if (event.target instanceof Element && event.target.closest("a[href]")) return;
      const ctx = resolveTextEditContext(event.target);
      if (ctx?.kind !== "selection") return;
      event.stopPropagation();
      open(event, textEditMenuItems(ctx));
    };

    const onBubble = (event: MouseEvent) => {
      if (event.defaultPrevented) return;
      const link = event.target instanceof Element ? event.target.closest("a[href]") : null;
      const linkItems = link ? linkMenuItems(link.getAttribute("href") ?? "") : [];
      if (linkItems.length) { open(event, linkItems); return; }
      const ctx = resolveTextEditContext(event.target);
      if (!ctx) return;
      open(event, textEditMenuItems(ctx));
    };

    document.addEventListener("contextmenu", onSelectionCapture, true);
    document.addEventListener("contextmenu", onBubble);
    document.addEventListener("keydown", dispatchKeyboardContextMenu);
    // Register after the editable listener in the bubble phase.
    if (!import.meta.env.DEV) setupContextMenuGuard();
    onCleanup(() => {
      document.removeEventListener("contextmenu", onSelectionCapture, true);
      document.removeEventListener("contextmenu", onBubble);
      document.removeEventListener("keydown", dispatchKeyboardContextMenu);
      teardownContextMenuGuard();
    });
  });

  return (
    <Show when={menu()} keyed>
      {(state) => (
        <ContextMenu
          anchor={state.anchor}
          items={state.items}
          retainFocus={state.retainFocus}
          onDismiss={() => setMenu(null)}
        />
      )}
    </Show>
  );
}
