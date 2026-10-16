/**
 * Hands focus back to the composer when resolving a card strands it: on the
 * document, inside the retiring card, or on a control that went away. Focus the
 * person moved somewhere usable is left alone.
 */
export function createResolvedFocusReturn(returnFocus: () => void) {
  let armed = false;
  let held: HTMLElement | null = null;
  let heldCard: string | null = null;
  const usable = (el: Element) => el.isConnected && !el.closest("[inert]");
  return {
    /** Records the control resolving card `cardId`; resolutions made without focus are ignored. */
    note(cardId?: string): void {
      const active = document.activeElement;
      held = active instanceof HTMLElement && active !== document.body ? active : null;
      heldCard = cardId ?? null;
      armed = held !== null;
    },
    /** Re-checks focus; `retired` once the card has left or stopped taking input. */
    settle(retired = false): void {
      if (!armed) return;
      const active = document.activeElement;
      const onDocument = !(active instanceof HTMLElement) || active === document.body;
      const stranded = onDocument || !usable(active);
      const card = !onDocument && heldCard ? active.closest("[data-checkpoint-id]") : null;
      const inResolvingCard = card?.getAttribute("data-checkpoint-id") === heldCard;
      if (!stranded && !inResolvingCard) {
        // A menu or the resolving control may still hold focus while the request is in flight.
        if (held && usable(held) && active === held) return;
        if (!retired && held && usable(held)) return;
        armed = false;
        return;
      }
      if (!stranded && !retired) return;
      if (!onDocument) (active as HTMLElement).blur();
      returnFocus();
      if (retired) armed = false;
    },
  };
}
