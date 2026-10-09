/** Scroll geometry tolerance. */
export const SCROLL_EPSILON_PX = 1;

/** Fractional viewport heights keep the tail aligned during resizing. */
export function scrollportClientHeight(viewport: HTMLElement): number {
  const rounded = viewport.clientHeight;
  const box = viewport.getBoundingClientRect().height;
  if (!(box > 0)) return rounded;
  // Borders and native scrollbars round away identically from both reads.
  const chrome = Math.max(0, viewport.offsetHeight - rounded);
  return Math.max(0, box - chrome);
}

/** Native offsets can differ by half a device pixel. */
export function devicePixelTolerance(view: Window | null): number {
  const ratio = view?.devicePixelRatio;
  return 0.5 / (ratio && ratio > 0 ? ratio : 1);
}

/** Maximum frames to wait for stable transcript geometry. */
export const TAIL_BOUND_CONFIRM_FRAMES = 30;

/** Fired when direct scrollport input begins. */
export const DEN_SCROLLPORT_INPUT_EVENT = "den:scrollport-input";

/** Marks the spacer that holds range past the painted tail. */
export const SCROLLPORT_EXTENT_HOLD_ATTR = "data-scrollport-extent-hold";

export type ScrollportCommitSource =
  | "thumb_drag"
  | "track_click"
  | "repin_tail"
  | "restore_anchor"
  | "layout_compensation"
  /** Carries the offset with content that moved above it. */
  | "content_shift"
  | "reveal"
  | "tab_inset"
  | "jump"
  /** Clamps an unclaimed offset to the painted content end. */
  | "tail_bound";

export type ScrollportCommitPosition = { top: number; left: number };
export type CommitHandler = (source: ScrollportCommitSource, position: ScrollportCommitPosition) => void;

/** Tail policy retained across scrollport bindings. */
export type ScrollportTailPolicy = {
  /** A stricter painted-content boundary than the scroll range. */
  resolveTail?: (naturalTailOffset: number) => number | null;
  /** Keeps the offset on the tail while it holds. */
  pinned?: () => boolean;
};
