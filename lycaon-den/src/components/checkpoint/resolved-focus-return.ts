/** Hands focus back when a resolved card retires or unmounts with the control that resolved it. */
export function createResolvedFocusReturn(returnFocus: () => void) {
  let held: HTMLElement | null = null;
  let heldCard: string | null = null;
  const usable = (el: Element) => el.isConnected && !el.closest("[inert]");
  return {
    /** Records the control resolving the card `cardId`; a menu that closes hands focus back into that card. */
    note(cardId?: string): void {
      const active = document.activeElement;
      held = active instanceof HTMLElement && active !== document.body ? active : null;
      heldCard = cardId ?? null;
    },
    settle(): void {
      const el = held;
      if (!el || usable(el)) return;
      const active = document.activeElement;
      const card = heldCard && active instanceof Element ? active.closest("[data-checkpoint-id]") : null;
      const inResolvingCard = card?.getAttribute("data-checkpoint-id") === heldCard;
      if (inResolvingCard && usable(card!)) return;
      held = null;
      if (active instanceof HTMLElement && active !== document.body && usable(active) && !inResolvingCard) return;
      if (active instanceof HTMLElement && active !== document.body) active.blur();
      returnFocus();
    },
  };
}
