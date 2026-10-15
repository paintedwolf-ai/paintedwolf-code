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

/**
 * What a card shows while its disclosures are closed: its text, names and
 * states, without the closed bodies themselves and without `aria-controls`,
 * which the WebKitGTK AT-SPI workaround sets only while a panel is mounted.
 */
export function collapsedFace(card: Element | null | undefined): string {
  if (!card) throw new Error("card not found");
  const copy = card.cloneNode(true) as Element;
  for (const closed of copy.querySelectorAll("[hidden]")) closed.remove();
  const named = [copy, ...copy.querySelectorAll("*")]
    .map((element) =>
      ["aria-label", "aria-expanded", "data-expanded", "data-count", "data-files", "data-status"]
        .map((name) => (element.hasAttribute(name) ? `${name}=${element.getAttribute(name)}` : ""))
        .filter(Boolean)
        .join(" "),
    )
    .filter(Boolean);
  return [copy.textContent?.replace(/\s+/g, " ").trim() ?? "", ...named].join("\n");
}

type MountedCard = { card: () => Element | null; unmount: () => void };

/** Renders a card on each side of the WebKitGTK AT-SPI workaround and expects one collapsed face. */
export async function expectSameCollapsedFace(
  mount: () => MountedCard,
  ready: (face: string) => boolean = () => true,
): Promise<void> {
  const { applyWebEngineFact } = await import("../platform/desktop/webkit-atspi-workaround.ts");
  const { waitFor } = await import("@solidjs/testing-library");
  const { expect } = await import("vitest");
  applyWebEngineFact(null);
  const off = mount();
  let expected = "";
  await waitFor(() => {
    expected = collapsedFace(off.card());
    expect(ready(expected)).toBe(true);
  });
  off.unmount();
  applyWebEngineFact({ engine: "webkitgtk", version: "2.54.1" });
  try {
    const on = mount();
    await waitFor(() => expect(collapsedFace(on.card())).toBe(expected));
    on.unmount();
  } finally {
    applyWebEngineFact(null);
  }
}
