"""Native AT-SPI scenarios whose state must be present before attributes are read."""

HOME_OVERFLOW = r"""
const opened = await __harness.openProject("Harness");
if (opened?.ok !== true) return { ok: false, error: "overflow project: " + JSON.stringify(opened) };
// Each held checkpoint belongs to a distinct chat, so this does not depend on
// state left by a previous WebKit release or diagnostic repeat.
for (let index = 0; index < 8; index++) {
  const session = await __harness.newSession();
  if (session?.ok !== true) return { ok: false, error: "overflow chat: " + JSON.stringify(session) };
  const held = await __harness.goto("checkpoint-held");
  if (held?.ok !== true) return { ok: false, error: "overflow checkpoint: " + JSON.stringify(held) };
}
const home = await __harness.goHome();
if (home?.ok !== true) return { ok: false, error: "home: " + JSON.stringify(home) };
let more;
for (let attempt = 0; attempt < 60; attempt++) {
  more = document.querySelector('[data-testid="attention-band-more"]');
  if (more && document.querySelectorAll('[data-testid="attention-band-row"]').length > 0) break;
  await new Promise((resolve) => setTimeout(resolve, 250));
}
const shown = document.querySelectorAll('[data-testid="attention-band-row"]').length;
if (!more || shown === 0) return { ok: false, error: "Home attention overflow did not mount" };
return { ok: true, attention: { seeded: 8, shown, overflow: more.textContent } };
"""


def diagnostic_scenario(body: str) -> str:
    """Capture the DOM selection before accessibility queries can end the process."""
    return "const reached = await (async () => {" + body + r"""})();
const selection = document.getSelection();
const describe = (node) => {
  const element = node?.nodeType === 1 ? node : node?.parentElement;
  if (!element) return null;
  const style = getComputedStyle(element);
  return {tag: element.tagName, testId: element.dataset.testid ?? null,
    classes: element.className, connected: element.isConnected,
    boxes: element.getClientRects().length, display: style.display, visibility: style.visibility,
    hiddenAncestor: !!element.closest('[hidden], [aria-hidden="true"], [inert]')};
};
return {...reached, diagnostics: {selection: selection ? {
  ranges: selection.rangeCount, collapsed: selection.isCollapsed,
  anchor: describe(selection.anchorNode), focus: describe(selection.focusNode),
  anchorOffset: selection.anchorOffset, focusOffset: selection.focusOffset
} : null}};
"""
