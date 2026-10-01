// Width bands invalidate styles only when a threshold is crossed.
import { onCleanup } from "solid-js";
import { observeSharedContentBox } from "./shared-resize-observer.ts";
import { SPLIT_MIN_HOST, STAGE_COL_MIN } from "../shell/stage-placement.ts";

/** One threshold. `px` and `rem` add, mirroring `calc(200px + 5.3rem)`. */
export type BandStop = {
  /** Token matched by CSS attribute selectors. */
  readonly token: string;
  readonly edge: "max" | "min";
  readonly px?: number;
  readonly rem?: number;
};

export type BandScale = {
  /** Attribute written on the observed element. */
  readonly attribute: string;
  readonly stops: readonly BandStop[];
};

export const LAYOUT_BAND_SCALES = {
  /** Width required for a stage beside the conversation. */
  stageHost: {
    attribute: "data-host-band",
    stops: [
      { token: "max-1076", edge: "max", px: SPLIT_MIN_HOST - 1 },
      { token: "min-1077", edge: "min", px: SPLIT_MIN_HOST },
    ],
  },
  /** Width required for the file tree beside the editor. */
  stageColumn: {
    attribute: "data-stage-band",
    stops: [
      { token: "max-735", edge: "max", px: STAGE_COL_MIN - 1 },
    ],
  },
  /** Full conversation width, including unsplit layouts. */
  chatColumn: {
    attribute: "data-chat-band",
    stops: [
      { token: "max-32rem", edge: "max", rem: 32 },
      { token: "max-34rem", edge: "max", rem: 34 },
      { token: "max-520", edge: "max", px: 520 },
    ],
  },
  browseStage: {
    attribute: "data-browse-band",
    stops: [
      { token: "max-560", edge: "max", px: 560 },
      { token: "max-620", edge: "max", px: 620 },
      { token: "max-700", edge: "max", px: 700 },
      { token: "max-850", edge: "max", px: 850 },
    ],
  },
  filesEditor: {
    attribute: "data-editor-band",
    stops: [
      { token: "max-320", edge: "max", px: 320 },
      { token: "max-520", edge: "max", px: 520 },
      { token: "max-620", edge: "max", px: 620 },
    ],
  },
  filesTree: {
    attribute: "data-tree-band",
    stops: [
      { token: "compact", edge: "max", px: 200, rem: 5.3 },
      { token: "max-219", edge: "max", px: 219 },
    ],
  },
  tabsPanel: {
    attribute: "data-panel-band",
    stops: [{ token: "min-640", edge: "min", px: 640 }],
  },
  diffsPage: {
    attribute: "data-diffs-band",
    stops: [
      { token: "max-360", edge: "max", px: 360 },
      { token: "max-520", edge: "max", px: 520 },
      { token: "max-640", edge: "max", px: 640 },
    ],
  },
} as const satisfies Record<string, BandScale>;

export function bandStopPx(stop: BandStop, remPx: number): number {
  return (stop.px ?? 0) + (stop.rem ?? 0) * remPx;
}

/** Tokens the width satisfies, in catalog order. */
export function bandTokens(
  scale: BandScale,
  widthPx: number,
  remPx: number,
): string {
  const tokens: string[] = [];
  for (const stop of scale.stops) {
    const edge = bandStopPx(stop, remPx);
    if (stop.edge === "max" ? widthPx <= edge : widthPx >= edge) {
      tokens.push(stop.token);
    }
  }
  return tokens.join(" ");
}

/** Cached until the reading scale changes. */
let remPx: number | undefined;

function rootRemPx(): number {
  if (remPx === undefined) {
    const root = typeof document === "undefined" ? undefined : document.documentElement;
    const size = root ? Number.parseFloat(getComputedStyle(root).fontSize) : Number.NaN;
    remPx = Number.isFinite(size) && size > 0 ? size : 16;
  }
  return remPx;
}

type Binding = { el: HTMLElement; scale: BandScale; width: number };

const bindings = new Set<Binding>();

function apply(binding: Binding): void {
  // Nothing to publish until the observer has reported a box.
  if (!Number.isFinite(binding.width)) return;
  const next = bandTokens(binding.scale, binding.width, rootRemPx());
  if (binding.el.getAttribute(binding.scale.attribute) === next) return;
  binding.el.setAttribute(binding.scale.attribute, next);
}

/** Reading-scale changes move rem-based thresholds. */
export function refreshLayoutBands(): void {
  remPx = undefined;
  for (const binding of bindings) apply(binding);
}

/** Publishes `scale` on `el` until the returned disposer runs. */
export function observeLayoutBand(el: HTMLElement, scale: BandScale): () => void {
  const binding: Binding = { el, scale, width: Number.NaN };
  bindings.add(binding);
  const stop = observeSharedContentBox(el, (box) => {
    binding.width = box.width;
    apply(binding);
  });
  return () => {
    stop();
    bindings.delete(binding);
  };
}

/** Ref-callback form: binds for the lifetime of the owning component. */
export function bindLayoutBand(el: HTMLElement, scale: BandScale): void {
  onCleanup(observeLayoutBand(el, scale));
}

/** Reset band state between tests. */
export function resetLayoutBandsForTests(): void {
  bindings.clear();
  remPx = undefined;
}
