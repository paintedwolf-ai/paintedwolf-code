import { isTauriRuntime } from "../runtime.ts";
import { windowChromeLayout, drawsWindowControls, type TitlebarAction } from "./window-chrome-layout.ts";

const DRAG_REGION = "data-tauri-drag-region";
const CLICKABLE_TAGS = new Set(["A", "BUTTON", "INPUT", "SELECT", "TEXTAREA", "LABEL", "SUMMARY"]);
const INTERACTIVE_ROLES = new Set(["button", "link", "menuitem", "tab", "checkbox", "radio", "switch", "option"]);

function isClickable(element: HTMLElement): boolean {
  return CLICKABLE_TAGS.has(element.tagName)
    || (element.hasAttribute("contenteditable") && element.getAttribute("contenteditable") !== "false")
    || (element.hasAttribute("tabindex") && element.getAttribute("tabindex") !== "-1")
    || INTERACTIVE_ROLES.has(element.getAttribute("role") ?? "");
}

/** The title bar the shell's drag regions form, by the same rules the shell's drag handling uses. */
export function isTitlebarTarget(path: readonly EventTarget[]): boolean {
  for (const target of path) {
    if (!(target instanceof HTMLElement)) continue;
    const region = target.getAttribute(DRAG_REGION);
    if (isClickable(target) && region === null) return false;
    if (region === null) continue;
    if (region === "false") return false;
    if (region === "deep") return true;
    if (region === "" || region === "true") return target === path[0];
  }
  return false;
}

async function perform(action: TitlebarAction): Promise<void> {
  if (action === "none") return;
  if (action === "lower" || action === "menu") {
    const { invoke } = await import("@tauri-apps/api/core");
    await invoke("linux_titlebar_action", { action });
    return;
  }
  const { getCurrentWindow } = await import("@tauri-apps/api/window");
  const window = getCurrentWindow();
  if (action === "minimize") await window.minimize();
  else await window.toggleMaximize();
}

function act(event: MouseEvent, action: TitlebarAction): void {
  // The shell's own drag handling would always toggle maximize; the desktop decides instead.
  event.preventDefault();
  event.stopImmediatePropagation();
  void perform(action).catch((error: unknown) => console.debug("[window-chrome] title bar action failed", action, error));
}

/**
 * Applies the desktop's title bar click actions to the drawn title bar on
 * Linux: double click, middle click, and the context menu click.
 */
export function followTitlebarClicks(target: Window = window): () => void {
  if (!isTauriRuntime() || !drawsWindowControls()) return () => undefined;
  const onMouseDown = (event: MouseEvent) => {
    if (!isTitlebarTarget(event.composedPath())) return;
    const actions = windowChromeLayout().actions;
    if (event.button === 0 && event.detail === 2) act(event, actions.double_click);
    else if (event.button === 1) act(event, actions.middle_click);
  };
  const onContextMenu = (event: MouseEvent) => {
    if (isTitlebarTarget(event.composedPath())) act(event, windowChromeLayout().actions.right_click);
  };
  target.addEventListener("mousedown", onMouseDown, true);
  target.addEventListener("contextmenu", onContextMenu, true);
  return () => {
    target.removeEventListener("mousedown", onMouseDown, true);
    target.removeEventListener("contextmenu", onContextMenu, true);
  };
}
