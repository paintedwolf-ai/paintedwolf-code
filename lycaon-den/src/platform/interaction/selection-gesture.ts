/**
 * A drag that selects a link's own text ends in a click on the link; that click
 * is not activation. Selections elsewhere in the document never block a link.
 */
export function clickSelectedText(event: MouseEvent, link: Element): boolean {
  // Keyboard and assistive activation dispatch clicks without pointer detail.
  if (event.detail === 0) return false;
  const selection = link.ownerDocument.getSelection();
  if (!selection || selection.isCollapsed || !selection.toString()) return false;
  for (let i = 0; i < selection.rangeCount; i += 1) {
    if (selection.getRangeAt(i).intersectsNode(link)) return true;
  }
  return false;
}
