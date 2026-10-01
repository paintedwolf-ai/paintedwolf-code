import { createEffect, onCleanup } from "solid-js";
import type { EditorView } from "@codemirror/view";
import {
  findController,
  registerFindableView,
  setPrimaryFindableView,
  type FindableView,
} from "./find-controller.ts";
import {
  registerFindRevealHost,
  type FindRevealHost,
} from "./find-reveal.ts";
import type { FindMatch } from "./find-match.ts";
import type { FindProviderKind } from "./find-provider.ts";
import { revealElementInScrollport } from "../platform/scrolling/scrollport-motion.ts";

export function scrollFindMatchIntoView(match: FindMatch): void {
  try {
    const node = match.range.startContainer;
    const el = node instanceof Element ? node : node.parentElement;
    if (!el) return;
    revealElementInScrollport(el);
  } catch {
    /* The match may detach. */
  }
}

export type BindFindableViewOptions = {
  id: string;
  root: () => HTMLElement | null | undefined;
  provider?: FindProviderKind;
  getEditorView?: () => EditorView | null | undefined;
  primary?: boolean | (() => boolean);
  enabled?: () => boolean;
  scrollMatchIntoView?: (match: FindMatch) => void;
};

export function bindFindableView(opts: BindFindableViewOptions): void {
  createEffect(() => {
    const enabled = opts.enabled?.() ?? true;
    const root = opts.root();
    if (!enabled || !root) return;

    const view: FindableView = {
      id: opts.id,
      provider: opts.provider ?? "dom",
      rootEl: () => opts.root() ?? null,
      scrollMatchIntoView:
        opts.scrollMatchIntoView ?? scrollFindMatchIntoView,
      ...(opts.getEditorView ? { getEditorView: opts.getEditorView } : {}),
    };
    const unregister = registerFindableView(view);
    const primary =
      typeof opts.primary === "function" ? opts.primary() : Boolean(opts.primary);
    if (primary) setPrimaryFindableView(opts.id);

    onCleanup(() => {
      unregister();
      if (primary && findController.primaryViewId() === opts.id) {
        setPrimaryFindableView(null);
      }
    });
  });
}

export type BindFindRevealHostOptions = {
  id: string;
  hostEl: () => HTMLElement | null | undefined;
  isCollapsed: () => boolean;
  revealForFind: () => () => void;
  collapsedCorpus?: () => string;
  collapsedCorpusAnchor?: () => Node | null | undefined;
};

export function bindFindRevealHost(opts: BindFindRevealHostOptions): void {
  createEffect(() => {
    const el = opts.hostEl();
    if (!el) return;

    // Track collapse state without re-registering the host.
    void opts.isCollapsed();

    const host: FindRevealHost = {
      id: opts.id,
      hostEl: () => opts.hostEl() ?? null,
      isCollapsed: opts.isCollapsed,
      revealForFind: opts.revealForFind,
      collapsedCorpus: opts.collapsedCorpus,
      collapsedCorpusAnchor: opts.collapsedCorpusAnchor,
    };
    const unregister = registerFindRevealHost(host);
    onCleanup(unregister);
  });
}
