/** Hands focus back when a resolved card retires or unmounts with the control that resolved it. */
export function createResolvedFocusReturn(returnFocus: () => void) {
  let held: HTMLElement | null = null;
  return {
    note(): void {
      const active = document.activeElement;
      held = active instanceof HTMLElement && active !== document.body ? active : null;
    },
    settle(): void {
      const el = held;
      if (!el || (el.isConnected && !el.closest("[inert]"))) return;
      held = null;
      const active = document.activeElement;
      if (active instanceof HTMLElement && active !== document.body && !active.closest("[inert]")) return;
      if (active instanceof HTMLElement && active !== document.body) active.blur();
      returnFocus();
    },
  };
}
