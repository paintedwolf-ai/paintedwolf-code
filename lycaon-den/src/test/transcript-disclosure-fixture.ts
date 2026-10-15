/** The button that opens a transcript disclosure card. */
export function disclosureToggle(card: Element | null | undefined): HTMLButtonElement {
  const toggle = card?.querySelector<HTMLButtonElement>(
    ":scope > .den-transcript-disclosure-toggle, :scope > .den-transcript-disclosure-header > .den-transcript-disclosure-toggle",
  );
  if (!toggle) throw new Error("transcript disclosure toggle not found");
  return toggle;
}

export function isDisclosureOpen(card: Element | null | undefined): boolean {
  return card?.getAttribute("data-expanded") === "true";
}
