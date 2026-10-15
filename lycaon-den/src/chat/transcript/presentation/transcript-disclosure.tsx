import { useTranscriptDisclosureStore } from "./disclosure-state.tsx";
import { Show, createSignal, createUniqueId, splitProps, type Accessor, type JSX } from "solid-js";
import { useTranscriptViewport } from "../../stream/transcript-viewport.tsx";
import {
  animateHeightToggle,
  heightToggleDirectionFor,
  type HeightToggleDirection,
} from "../../../ui/height-toggle-motion.ts";
import type { TranscriptDisclosureKey } from "./transcript-disclosure-key.ts";
import { isTranscriptDisclosureOpen, isTranscriptDisclosureShell } from "./transcript-disclosure-shell.ts";

export const TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT =
  "den:transcript-disclosure-layout";

function notifyDisclosureLayout(shell: HTMLElement): void {
  shell.dispatchEvent(
    new Event(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, { bubbles: true }),
  );
}

function setBodiesHidden(shell: HTMLElement, header: HTMLElement, hidden: boolean): void {
  for (const child of shell.children) {
    if (!(child instanceof HTMLElement) || child === header) continue;
    if (hidden) child.style.display = "none";
    else child.style.removeProperty("display");
  }
}

/** The shell's child that stays visible while the disclosure is closed. */
function disclosureHeader(toggle: HTMLElement): { shell: HTMLElement; header: HTMLElement } | null {
  let header = toggle;
  for (let parent = toggle.parentElement; parent; parent = parent.parentElement) {
    if (isTranscriptDisclosureShell(parent)) return { shell: parent, header };
    header = parent;
  }
  return null;
}

export function animateDisclosureHeight(
  shell: HTMLElement,
  header: HTMLElement,
  applyOpen: (next: boolean) => void,
  opts: {
    direction?: HeightToggleDirection;
    onSettled?: () => void;
  },
): void {
  const direction = opts.direction ?? heightToggleDirectionFor(shell, isTranscriptDisclosureOpen(shell));
  animateHeightToggle(shell, {
    direction,
    showBody: () => {
      setBodiesHidden(shell, header, false);
      applyOpen(true);
    },
    hideBody: () => setBodiesHidden(shell, header, true),
    closeBody: () => applyOpen(false),
    resetBodyVisibility: () => setBodiesHidden(shell, header, false),
    collapsedHeight: () => header.getBoundingClientRect().height,
    bodyStillOpen: () => isTranscriptDisclosureOpen(shell),
    onSettled: () => opts.onSettled?.(),
  });
  notifyDisclosureLayout(shell);
}

export type TranscriptDisclosure = {
  key: TranscriptDisclosureKey | undefined;
  open: Accessor<boolean>;
  /** Id of the panel the toggle controls. */
  panelId: string;
  /** Animates the disclosure that contains the clicked toggle. */
  onToggleClick: JSX.EventHandler<HTMLElement, MouseEvent>;
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

  const onToggleClick: JSX.EventHandler<HTMLElement, MouseEvent> = (event) => {
    const found = disclosureHeader(event.currentTarget);
    if (!found) return;
    const { shell, header } = found;
    const direction = heightToggleDirectionFor(shell, open());
    const finishMotion = viewport?.beginDisclosureMotion(currentKey(), direction);
    animateDisclosureHeight(shell, header, applyOpen, {
      direction,
      onSettled: () => {
        notifyDisclosureLayout(shell);
        finishMotion?.();
      },
    });
  };

  return {
    get key() {
      return currentKey();
    },
    open,
    panelId: createUniqueId(),
    onToggleClick,
    applyOpen,
    revealTemporarily,
  };
}

type ShellProps = Omit<JSX.HTMLAttributes<HTMLDivElement>, "ref"> & {
  disclosure: TranscriptDisclosure;
  ref?: (element: HTMLDivElement) => void;
};

/** A transcript card that opens and closes through its toggle. */
export function TranscriptDisclosureShell(props: ShellProps) {
  const [local, rest] = splitProps(props, ["disclosure", "ref", "children"]);
  return (
    <div
      {...rest}
      ref={(element) => local.ref?.(element)}
      data-transcript-disclosure=""
      data-disclosure-key={local.disclosure.key}
      data-expanded={local.disclosure.open() ? "true" : "false"}
    >
      {local.children}
    </div>
  );
}

type ToggleProps = Omit<JSX.ButtonHTMLAttributes<HTMLButtonElement>, "ref" | "type" | "class"> & {
  disclosure: TranscriptDisclosure;
  ref?: (element: HTMLButtonElement) => void;
  class?: string;
  /** True when a header row holds the toggle beside other controls. */
  inHeader?: boolean;
};

/** The button that opens a transcript card; it is the card header unless `inHeader`. */
export function TranscriptDisclosureToggle(props: ToggleProps) {
  const [local, rest] = splitProps(props, ["disclosure", "ref", "class", "inHeader", "onClick", "children"]);
  const classes = () =>
    ["den-transcript-disclosure-toggle", local.inHeader ? "" : "den-transcript-disclosure-header", local.class ?? ""]
      .filter(Boolean)
      .join(" ");
  return (
    <button
      {...rest}
      ref={(element) => local.ref?.(element)}
      type="button"
      class={classes()}
      aria-expanded={local.disclosure.open()}
      aria-controls={local.disclosure.open() ? local.disclosure.panelId : undefined}
      onClick={(event) => {
        if (typeof local.onClick === "function") local.onClick(event);
        else local.disclosure.onToggleClick(event);
      }}
    >
      {local.children}
    </button>
  );
}

type PanelProps = JSX.HTMLAttributes<HTMLDivElement> & { disclosure: TranscriptDisclosure };

/**
 * The content a transcript card reveals, mounted only while the card is open:
 * WebKitGTK aborts a screen reader's text query on content ending in a hidden subtree.
 */
export function TranscriptDisclosurePanel(props: PanelProps) {
  const [local, rest] = splitProps(props, ["disclosure", "children"]);
  return (
    <Show when={local.disclosure.open()}>
      <div {...rest} id={local.disclosure.panelId}>
        {local.children}
      </div>
    </Show>
  );
}
