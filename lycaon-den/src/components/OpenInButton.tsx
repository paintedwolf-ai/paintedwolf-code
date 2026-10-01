import { Show, createSignal, createMemo, createEffect, onCleanup } from "solid-js";
import { CONTEXT_ACTIONS } from "./context-action-catalog.ts";
import { ContextMenu, type ContextMenuAnchor } from "./ContextMenu.tsx";
import { openInDestinationItems } from "./open-in-menu-items.ts";
import { bindChromeContextMenu } from "./context-menu-open.ts";
import { DenButton } from "./primitives/DenButton.tsx";
import { localPathOnDevice, type LocalPathTarget } from "../platform/navigation/open-local-path.ts";

type Props = {
  target: LocalPathTarget | null;
  compact?: boolean;
  disabled?: boolean;
  onOpenChange?: (open: boolean) => void;
};

export function OpenInButton(props: Props) {
  const target = createMemo(() => props.target);
  const available = createMemo(() => {
    const current = target();
    return current != null && localPathOnDevice(current);
  });
  const [menu, setMenu] = createSignal<{ anchor: ContextMenuAnchor; target: LocalPathTarget } | null>(null);
  const open = (anchor: ContextMenuAnchor) => {
    const captured = target();
    if (!captured || props.disabled) return;
    setMenu({ anchor, target: captured });
    props.onOpenChange?.(true);
  };
  const dismiss = () => {
    setMenu(null);
    props.onOpenChange?.(false);
  };
  createEffect(() => {
    if (menu() && !available()) dismiss();
  });
  onCleanup(() => { if (menu()) props.onOpenChange?.(false); });
  const binding = bindChromeContextMenu(open);
  return (
    <Show when={available()}>
      <DenButton
        variant="secondary"
        compact={props.compact}
        disabled={props.disabled}
        data-testid="open-in-button"
        aria-haspopup="menu"
        aria-expanded={menu() != null}
        onContextMenu={binding.onContextMenu}
        onKeyDown={binding.onKeyDown}
        onClick={(event) => open(event.currentTarget)}
      >
        {CONTEXT_ACTIONS.openIn.label}…
      </DenButton>
      <Show when={menu()} keyed>
        {(state) => <ContextMenu anchor={state.anchor}
          items={openInDestinationItems(state.target)} onDismiss={dismiss} />}
      </Show>
    </Show>
  );
}
