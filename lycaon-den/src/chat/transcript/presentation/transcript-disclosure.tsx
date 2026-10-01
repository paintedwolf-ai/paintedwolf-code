import { useTranscriptDisclosureStore } from "./disclosure-state.tsx";
import { createSignal, type Accessor, type JSX } from "solid-js";
import { useTranscriptViewport } from "../../stream/transcript-viewport.tsx";
import {
  animateHeightToggle,
  heightToggleDirectionFor,
  isHeightToggleAnimating,
  type HeightToggleDirection,
} from "../../../ui/height-toggle-motion.ts";
import type { TranscriptDisclosureKey } from "./transcript-disclosure-key.ts";

export const TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT =
  "den:transcript-disclosure-layout";

function notifyDisclosureLayout(details: HTMLDetailsElement): void {
  details.dispatchEvent(
    new Event(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, { bubbles: true }),
  );
}

function detailsBodies(details: HTMLDetailsElement): HTMLElement[] {
  return Array.from(details.children).filter(
    (child): child is HTMLElement =>
      child instanceof HTMLElement && child.tagName !== "SUMMARY",
  );
}

function setDetailsBodiesHidden(details: HTMLDetailsElement, hidden: boolean): void {
  for (const body of detailsBodies(details)) {
    if (hidden) body.style.display = "none";
    else body.style.removeProperty("display");
  }
}

export function animateDisclosureHeight(
  details: HTMLDetailsElement,
  summary: HTMLElement,
  applyOpen: (next: boolean) => void,
  opts: {
    direction?: HeightToggleDirection;
    onSettled?: () => void;
  },
): void {
  const direction = opts.direction ?? heightToggleDirectionFor(details, details.open);
  animateHeightToggle(details, {
    direction,
    showBody: () => {
      setDetailsBodiesHidden(details, false);
      details.open = true;
      applyOpen(true);
    },
    hideBody: () => setDetailsBodiesHidden(details, true),
    closeBody: () => {
      applyOpen(false);
      details.open = false;
    },
    resetBodyVisibility: () => setDetailsBodiesHidden(details, false),
    collapsedHeight: () => summary.getBoundingClientRect().height,
    bodyStillOpen: () => details.open,
    onSettled: () => opts.onSettled?.(),
  });
  notifyDisclosureLayout(details);
}

type TranscriptDisclosure = {
  key: TranscriptDisclosureKey | undefined;
  open: Accessor<boolean>;
  onToggle: (event: Event) => void;
  onSummaryClick: JSX.EventHandler<HTMLElement, MouseEvent>;
  applyOpen: (next: boolean) => void;
  revealTemporarily: () => () => void;
};

/** Keyless disclosures keep their own state; keyed ones share the transcript's open state. */
export function useTranscriptDisclosure(
  disclosureKey?: () => TranscriptDisclosureKey | undefined,
): TranscriptDisclosure {
  const viewport = useTranscriptViewport();
  const store = useTranscriptDisclosureStore() ?? viewport?.disclosures;
  const currentKey = () => disclosureKey?.();
  const [localOpen, setLocalOpen] = createSignal(false);

  const open = () => {
    const key = currentKey();
    return key && store ? store.isOpen(key) : localOpen();
  };

  const applyOpen = (next: boolean) => {
    const key = currentKey();
    if (key && store) store.setUserOpen(key, next);
    else setLocalOpen(next);
  };

  const revealTemporarily = (): (() => void) => {
    const key = currentKey();
    if (key && store) return store.acquire(key);
    if (localOpen()) return () => {};
    setLocalOpen(true);
    return () => setLocalOpen(false);
  };

  const onToggle = (event: Event) => {
    const details = event.currentTarget;
    if (!(details instanceof HTMLDetailsElement)) return;
    if (isHeightToggleAnimating(details)) return;
    if (details.open === open()) return;
    const key = currentKey();
    const direction = details.open ? "open" : "close";
    const finishMotion = viewport?.beginDisclosureMotion(key, direction);
    applyOpen(details.open);
    notifyDisclosureLayout(details);
    finishMotion?.();
  };

  const onSummaryClick: JSX.EventHandler<HTMLElement, MouseEvent> = (event) => {
    const summary = event.currentTarget;
    const details = summary.parentElement;
    if (!(details instanceof HTMLDetailsElement)) return;
    // Synthetic non-cancelable clicks keep the native details default action.
    if (event.cancelable === false) return;
    event.preventDefault();
    const direction = heightToggleDirectionFor(details, details.open);
    const finishMotion = viewport?.beginDisclosureMotion(currentKey(), direction);
    const settle = () => {
      notifyDisclosureLayout(details);
      finishMotion?.();
    };

    animateDisclosureHeight(details, summary, applyOpen, {
      direction,
      onSettled: settle,
    });
  };

  return {
    get key() {
      return currentKey();
    },
    open,
    onToggle,
    onSummaryClick,
    applyOpen,
    revealTemporarily,
  };
}
