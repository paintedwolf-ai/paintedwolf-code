/** Marks a transcript disclosure card; `data-expanded` on it carries the committed open state. */
const TRANSCRIPT_DISCLOSURE_ATTR = "data-transcript-disclosure";

export function isTranscriptDisclosureShell(element: Element): boolean {
  return element.hasAttribute(TRANSCRIPT_DISCLOSURE_ATTR);
}

export function isTranscriptDisclosureOpen(shell: Element): boolean {
  return shell.getAttribute("data-expanded") === "true";
}
