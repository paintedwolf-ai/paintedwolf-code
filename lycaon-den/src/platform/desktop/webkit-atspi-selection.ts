import { webkitAtspiWorkaroundActive } from "./webkit-atspi-workaround.ts";

type Intent = {
  anchor: Node;
  anchorOffset: number;
  focus: Node;
  focusOffset: number;
};

/** Keep logical endpoints outside WebKit's live selection while the surface is inaccessible. */
export function createRetainedWebKitSelection(surface: HTMLElement) {
  let parkedDocument: Document | undefined;
  let intent: Intent | undefined;
  let parked = false;
  const changed = () => { intent = undefined; };
  const content = new MutationObserver(changed);
  const selectionChanged = () => {
    if (parked && parkedDocument?.getSelection()?.rangeCount) changed();
  };

  const stop = () => {
    const mutated = content.takeRecords().length > 0;
    content.disconnect();
    parkedDocument?.removeEventListener("selectionchange", selectionChanged);
    parkedDocument = undefined;
    parked = false;
    if (mutated) changed();
  };
  const release = () => {
    const selection = surface.ownerDocument.getSelection();
    if (selection && (surface.contains(selection.anchorNode) || surface.contains(selection.focusNode))) {
      selection.removeAllRanges();
    }
  };

  return {
    hide() {
      if (!webkitAtspiWorkaroundActive() || parked) return;
      const selection = surface.ownerDocument.getSelection();
      intent = undefined;
      if (selection?.anchorNode && selection.focusNode && !selection.isCollapsed &&
          surface.contains(selection.anchorNode) && surface.contains(selection.focusNode)) {
        intent = {
          anchor: selection.anchorNode, anchorOffset: selection.anchorOffset,
          focus: selection.focusNode, focusOffset: selection.focusOffset,
        };
      }
      parked = true;
      content.observe(surface, { subtree: true, childList: true, characterData: true });
      parkedDocument = surface.ownerDocument;
      parkedDocument.addEventListener("selectionchange", selectionChanged);
      release();
    },
    restore() {
      const previousDocument = parkedDocument;
      stop();
      const saved = intent;
      intent = undefined;
      if (!saved || !webkitAtspiWorkaroundActive() || !surface.isConnected ||
          previousDocument !== surface.ownerDocument) return;
      const selection = surface.ownerDocument.getSelection();
      if (!selection || selection.rangeCount > 0) return;
      const focused = surface.ownerDocument.activeElement;
      if (focused instanceof HTMLElement && !surface.contains(focused) &&
          (focused.isContentEditable || focused instanceof HTMLInputElement || focused instanceof HTMLTextAreaElement)) return;
      if (!saved.anchor.isConnected || !saved.focus.isConnected ||
          !surface.contains(saved.anchor) || !surface.contains(saved.focus)) return;
      selection.setBaseAndExtent(saved.anchor, saved.anchorOffset, saved.focus, saved.focusOffset);
    },
    dispose() {
      stop();
      intent = undefined;
      if (webkitAtspiWorkaroundActive()) release();
    },
  };
}
