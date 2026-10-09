import { isShellLayoutUnstable } from "../../../shell/shell-layout-busy.ts";

type StreamEngagement = {
  suppressEngageUntil: number;
  lastScrollTop?: number;
  activePointer: boolean;
  /**
   * The active press selects rather than scrolls: a mouse or pen press on content, or
   * any press once it has made a selection. It may release following, never resume it.
   */
  selectionPress?: boolean;
  /** A scrollbar thumb drag is moving the stream. */
  scrollbarGesture?: boolean;
  /** Shared frame sample that avoids a layout read in the wheel handler. */
  cachedVerticalOverflow?: boolean;
  /** Last reader input able to scroll the stream up. */
  lastUpwardIntentAt?: number;
  /** Last reader input able to scroll the stream down. */
  lastDownwardIntentAt?: number;
  /** Scroll range at the last sample; a change moves the scrollbar thumb. */
  lastScrollHeight?: number;
};

const streamEngagementByTarget = new WeakMap<HTMLElement, StreamEngagement>();

export function streamEngagement(streamTarget: HTMLElement): StreamEngagement {
  let state = streamEngagementByTarget.get(streamTarget);
  if (!state) {
    state = { suppressEngageUntil: 0, activePointer: false };
    streamEngagementByTarget.set(streamTarget, state);
  }
  return state;
}

/** Offset changes during this window are not read as reader motion. */
export function suppressStreamScrollEngagement(
  streamTarget: HTMLElement,
  durationMs: number,
): void {
  streamEngagement(streamTarget).suppressEngageUntil =
    performance.now() + durationMs;
}

export function isStreamScrollEngagementSuppressed(streamTarget: HTMLElement): boolean {
  return (
    isShellLayoutUnstable() ||
    performance.now() < streamEngagement(streamTarget).suppressEngageUntil
  );
}
