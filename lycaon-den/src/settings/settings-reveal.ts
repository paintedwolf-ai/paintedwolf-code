import { createEffect, createSignal, untrack } from "solid-js";
import { focusWithoutScroll } from "../platform/interaction/focus.ts";
import { revealElementInScrollport } from "../platform/scrolling/scrollport-motion.ts";
import { flashRevealTarget } from "../ui/reveal-flash.ts";
import {
  SETTING_ANCHOR_ATTRIBUTE,
  SETTING_CONTROL_ATTRIBUTE,
  type SettingDefinition,
  type SettingsSectionTabs,
  type TabbedSettingsSection,
} from "./settings-registry.ts";

/** How long a request waits for its section, tab, and row to render. */
export const SETTING_REVEAL_TIMEOUT_MS = 8000;

export type SettingRevealRequest = {
  readonly setting: SettingDefinition;
  readonly expiresAt: number;
};

const [pending, setPending] = createSignal<SettingRevealRequest | null>(null);

/** The request Settings has yet to land, or null. */
export const pendingSettingReveal = pending;

/**
 * Asks Settings to land on one row. The caller opens the section; the section's
 * panel selects the tab, and the open Settings view reveals the row.
 */
export function requestSettingReveal(
  setting: SettingDefinition,
): SettingRevealRequest {
  const request = {
    setting,
    expiresAt: Date.now() + SETTING_REVEAL_TIMEOUT_MS,
  };
  setPending(request);
  // Expiry keeps a request whose section never opened from landing on a later visit.
  window.setTimeout(() => settleSettingReveal(request), SETTING_REVEAL_TIMEOUT_MS);
  return request;
}

export function settleSettingReveal(request: SettingRevealRequest): void {
  if (untrack(pending) === request) setPending(null);
}

/** Lets a tabbed panel select the tab a pending reveal targets. */
export function followSettingRevealTab<S extends TabbedSettingsSection>(
  section: S,
  select: (tab: SettingsSectionTabs[S]) => void,
): void {
  createEffect(() => {
    const setting = pending()?.setting;
    if (setting?.section !== section || setting.tab === undefined) return;
    const tab = setting.tab as SettingsSectionTabs[S];
    untrack(() => select(tab));
  });
}

const HIDING_ANCESTOR = "[inert], [hidden], [aria-hidden='true']";
const FOCUSABLE =
  "input:not([type='hidden']), select, textarea, button, a[href], [tabindex]";

function escapeAttribute(value: string): string {
  const escape = (globalThis as { CSS?: { escape?: (v: string) => string } }).CSS?.escape;
  return escape ? escape(value) : value.replace(/["\\]/g, "\\$&");
}

/** The row for `id` once nothing above it hides it or makes it inert. */
function presentedAnchor(root: HTMLElement, id: string): HTMLElement | null {
  const selector = `[${SETTING_ANCHOR_ATTRIBUTE}="${escapeAttribute(id)}"]`;
  for (const element of root.querySelectorAll<HTMLElement>(selector)) {
    if (element.isConnected && !element.closest(HIDING_ANCESTOR)) return element;
  }
  return null;
}

function waitForAnchor(
  root: HTMLElement,
  id: string,
  timeoutMs: number,
  isCurrent: () => boolean,
): Promise<HTMLElement | null> {
  const immediate = presentedAnchor(root, id);
  if (immediate || timeoutMs <= 0) return Promise.resolve(immediate);
  return new Promise((resolve) => {
    let timeout = 0;
    const finish = (element: HTMLElement | null) => {
      clearTimeout(timeout);
      observer.disconnect();
      resolve(element);
    };
    const observer = new MutationObserver(() => {
      if (!isCurrent()) return finish(null);
      const element = presentedAnchor(root, id);
      if (element) finish(element);
    });
    // Surfaces above the view publish by dropping inert and aria-hidden.
    observer.observe(root.ownerDocument.documentElement, {
      subtree: true,
      childList: true,
      attributes: true,
      attributeFilter: ["inert", "hidden", "aria-hidden", SETTING_ANCHOR_ATTRIBUTE],
    });
    timeout = window.setTimeout(
      () => finish(isCurrent() ? presentedAnchor(root, id) : null),
      timeoutMs,
    );
  });
}

function nextPaint(): Promise<void> {
  return new Promise((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
  );
}

function focusable(element: HTMLElement): boolean {
  return (
    element.matches(FOCUSABLE) &&
    !element.matches(":disabled") &&
    element.tabIndex >= 0
  );
}

/** Focus the row's primary control, or the row itself when it has none enabled. */
function focusSettingControl(anchor: HTMLElement): HTMLElement | null {
  const named = anchor.querySelector<HTMLElement>(`[${SETTING_CONTROL_ATTRIBUTE}]`);
  const candidates = named
    ? [named]
    : [anchor, ...anchor.querySelectorAll<HTMLElement>(FOCUSABLE)];
  const control = candidates.find(focusable);
  if (control && focusWithoutScroll(control)) return control;
  if (!anchor.hasAttribute("tabindex")) anchor.tabIndex = -1;
  return focusWithoutScroll(anchor);
}

/**
 * Waits for a setting's row inside `root`, scrolls it into view, flashes it, and
 * focuses its control. Resolves false when the row never presents in time.
 */
export async function revealSettingAnchor(
  root: HTMLElement,
  id: string,
  opts: { timeoutMs: number; isCurrent: () => boolean },
): Promise<boolean> {
  const anchor = await waitForAnchor(root, id, opts.timeoutMs, opts.isCurrent);
  if (!anchor || !opts.isCurrent()) return false;
  await nextPaint();
  if (!anchor.isConnected || !opts.isCurrent()) return false;
  revealElementInScrollport(anchor, { block: "center" });
  flashRevealTarget(anchor);
  focusSettingControl(anchor);
  return true;
}
