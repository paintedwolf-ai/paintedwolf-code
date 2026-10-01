import { isPresented } from "../../ui/presented.ts";

const FOCUSABLE =
  'a[href],button,textarea,input,select,[contenteditable]:not([contenteditable="false"]),[tabindex]';

export type FocusTrapOptions = {
  /** Optional Escape action. */
  onEscape?: () => void;
  /** Focus target after deactivation. */
  returnFocus?: HTMLElement | null;
  /** Preferred initial focus target. */
  initialFocus?: HTMLElement | null;
  /** Moves focus on activation unless false. */
  autoFocus?: boolean;
  /** Background elements made inert while active. */
  inertTarget?: HTMLElement | HTMLElement[] | null;
};

export type FocusTrapHandle = {
  deactivate: (options?: { restoreFocus?: boolean }) => void;
};

function isFocusable(el: HTMLElement): boolean {
  if (el.matches(":disabled")) return false;
  if (el instanceof HTMLInputElement && el.type === "hidden") return false;
  if (!isPresented(el)) return false;
  const style = getComputedStyle(el);
  if (style.display === "none" || style.visibility === "hidden") return false;
  if (el.hasAttribute("tabindex") && el.tabIndex < 0) return false;
  const editable = el.matches(
    '[contenteditable]:not([contenteditable="false"])',
  );
  if (el.tabIndex < 0 && !editable) return false;
  return true;
}

export function focusableElements(container: HTMLElement): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(isFocusable);
}

type InertClaimState = { count: number; wasInert: boolean };
const inertClaims = new WeakMap<HTMLElement, InertClaimState>();

function claimInert(
  targets: HTMLElement | HTMLElement[] | null | undefined,
): () => void {
  if (!targets) return () => {};
  const list = Array.isArray(targets) ? targets : [targets];
  for (const el of list) {
    if (!el) continue;
    const current = inertClaims.get(el);
    if (current) {
      current.count += 1;
      continue;
    }
    inertClaims.set(el, { count: 1, wasInert: el.hasAttribute("inert") });
    el.setAttribute("inert", "");
  }
  let released = false;
  return () => {
    if (released) return;
    released = true;
    for (const el of list) {
      if (!el) continue;
      const current = inertClaims.get(el);
      if (!current) continue;
      current.count -= 1;
      if (current.count > 0) continue;
      inertClaims.delete(el);
      if (!current.wasInert) el.removeAttribute("inert");
    }
  };
}

export function activateFocusTrap(
  container: HTMLElement,
  options: FocusTrapOptions = {},
): FocusTrapHandle {
  const previouslyFocused =
    options.returnFocus ??
    (document.activeElement instanceof HTMLElement ? document.activeElement : null);

  const inertTarget = options.inertTarget ?? null;
  const releaseInert = claimInert(inertTarget);
  let active = true;

  const focusInitial = () => {
    if (!active) return;
    const preferred = options.initialFocus;
    if (preferred && container.contains(preferred) && isFocusable(preferred)) {
      preferred.focus({ preventScroll: true });
      return;
    }
    const first = focusableElements(container)[0];
    if (first) {
      first.focus({ preventScroll: true });
      return;
    }
    if (!container.hasAttribute("tabindex")) {
      container.tabIndex = -1;
    }
    container.focus({ preventScroll: true });
  };

  // Defer until portal content mounts.
  if (options.autoFocus !== false) {
    queueMicrotask(focusInitial);
  }

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape" && options.onEscape) {
      e.preventDefault();
      e.stopPropagation();
      options.onEscape();
      return;
    }
    if (e.key !== "Tab") return;
    const list = focusableElements(container);
    if (list.length === 0) {
      e.preventDefault();
      container.focus({ preventScroll: true });
      return;
    }
    const first = list[0]!;
    const last = list[list.length - 1]!;
    const active = document.activeElement;
    if (e.shiftKey) {
      if (active === first || !container.contains(active)) {
        e.preventDefault();
        last.focus({ preventScroll: true });
      }
      return;
    }
    if (active === last || !container.contains(active)) {
      e.preventDefault();
      first.focus({ preventScroll: true });
    }
  };

  container.addEventListener("keydown", onKeyDown);

  return {
    deactivate(deactivateOptions = {}) {
      if (!active) return;
      active = false;
      container.removeEventListener("keydown", onKeyDown);
      releaseInert();
      if (
        deactivateOptions.restoreFocus !== false &&
        previouslyFocused?.isConnected
      ) {
        previouslyFocused.focus({ preventScroll: true });
      }
    },
  };
}
