import { createEffect, onCleanup } from "solid-js";

/** Outside clicks and keyboard navigation dismiss the Layout tray. */
export function dismissLayoutDockOnOutsidePress(input: {
  open: () => boolean;
  dock: () => HTMLElement | undefined;
  onDismiss: () => void;
}): void {
  createEffect(() => {
    if (!input.open()) return;
    let keyboardNavigation = false;
    const dismissOutside = (target: EventTarget | null) => {
      if (target instanceof Node && input.dock()?.contains(target)) return;
      input.onDismiss();
    };
    const onPress = (event: PointerEvent) => {
      keyboardNavigation = false;
      dismissOutside(event.target);
    };
    const onKeyDown = () => { keyboardNavigation = true; };
    const onKeyUp = () => { keyboardNavigation = false; };
    const onFocus = (event: FocusEvent) => {
      // Stage activation can restore focus behind the open tray.
      if (keyboardNavigation) dismissOutside(event.target);
    };
    document.addEventListener("pointerdown", onPress, true);
    document.addEventListener("focusin", onFocus, true);
    document.addEventListener("keydown", onKeyDown, true);
    document.addEventListener("keyup", onKeyUp, true);
    onCleanup(() => {
      document.removeEventListener("pointerdown", onPress, true);
      document.removeEventListener("focusin", onFocus, true);
      document.removeEventListener("keydown", onKeyDown, true);
      document.removeEventListener("keyup", onKeyUp, true);
    });
  });
}
