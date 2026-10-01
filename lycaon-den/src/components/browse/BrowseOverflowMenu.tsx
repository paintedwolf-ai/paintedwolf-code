import { Show, createEffect, createMemo, createSignal } from "solid-js";
import { ContextMenu, type ContextMenuAnchor, type ContextMenuItem } from "../ContextMenu.tsx";
import { bindChromeContextMenu } from "../context-menu-open.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { DenMenuTrigger } from "../primitives/DenMenuTrigger.tsx";

type BrowseOverflowMenuProps = {
  items: readonly ContextMenuItem[];
  testId?: string;
  label?: string;
  variant?: "chip" | "icon";
  disabled?: boolean;
};

export function BrowseOverflowMenu(props: BrowseOverflowMenuProps) {
  const items = createMemo(() => props.items);
  const [menu, setMenu] = createSignal<{ anchor: ContextMenuAnchor; items: readonly ContextMenuItem[] } | null>(null);
  let triggerEl: HTMLButtonElement | undefined;
  const testId = () => props.testId ?? "browse-overflow";
  const label = () => props.label ?? "More";
  const close = () => setMenu(null);
  const open = (anchor: ContextMenuAnchor) => {
    if (props.disabled) return;
    triggerEl?.focus({ preventScroll: true });
    setMenu({ anchor, items: items() });
  };
  const toggle = () => {
    if (menu()) close();
    else if (triggerEl) open(triggerEl);
  };
  const binding = bindChromeContextMenu(open);
  createEffect(() => { if (props.disabled) close(); });

  return <>
    <Show when={props.variant === "icon"} fallback={
      <DenMenuTrigger label={label()} open={menu() != null} popup="menu"
        testId={`${testId()}-trigger`} disabled={props.disabled}
        buttonRef={(el) => { triggerEl = el; }} onClick={toggle}
        onKeyDown={binding.onKeyDown} onContextMenu={binding.onContextMenu} />
    }>
      <button type="button" class="den-browse-overflow__icon-trigger den-inset-icon-btn"
        data-testid={`${testId()}-trigger`} aria-label={label()} aria-haspopup="menu"
        aria-expanded={menu() != null} disabled={props.disabled}
        ref={(el) => { triggerEl = el; }} onClick={toggle}
        onKeyDown={binding.onKeyDown} onContextMenu={binding.onContextMenu}>
        <ThemeIcon slot="more" size={16} />
      </button>
    </Show>
    <Show when={menu()} keyed>{(state) =>
      <ContextMenu anchor={state.anchor} items={state.items} label={label()}
        align="end" gap={6} onDismiss={close} />
    }</Show>
  </>;
}
