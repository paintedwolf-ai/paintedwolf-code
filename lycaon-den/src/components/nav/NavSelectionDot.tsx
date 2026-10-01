import { createEffect, onCleanup } from "solid-js";
import { FrameMeasurements } from "../../layout/frame-measurements.ts";
import { perfMark } from "../../chat/stream/den-main-thread-perf.ts";
import {
  isShellLayoutBusy,
  onShellLayoutSettled,
} from "../../shell/shell-layout-busy.ts";
import {
  playSelectionTravel,
  shouldSnapSelectionTravel,
} from "../../ui/selection-travel.ts";

type DotPosition = { dot: HTMLSpanElement; y: number | null; animate: boolean };
type DotPlacement = {
  read(): DotPosition | undefined;
  write(position: DotPosition): void;
};
const placements = new FrameMeasurements(
  (placement: DotPlacement) => placement.read(),
  (placement, position) => { if (position) placement.write(position); },
);

const NAV_ROW_ATTR = "data-nav-row";

/** Marks a stable row for selection travel. */
export function navRow(index: number): Record<string, number> {
  return { [NAV_ROW_ATTR]: index };
}

type Props = {
  /** Active row index within the list; negative hides the dot. */
  index: number;
};

/** Centers the marker using the row's list-relative offset. */
function centerYInList(row: HTMLElement, dotHeight: number): number {
  return row.offsetTop + (row.offsetHeight - dotHeight) / 2;
}

/** Height used before layout resolves the dot. */
const DOT_FALLBACK_HEIGHT_PX = 11;

function measureDotHeightPx(dot: HTMLElement): number {
  return (
    dot.offsetHeight ||
    Number.parseFloat(getComputedStyle(dot).height) ||
    DOT_FALLBACK_HEIGHT_PX
  );
}

/** Moves one marker across measured navigation rows. */
export function NavSelectionDot(props: Props) {
  let dotRef: HTMLSpanElement | undefined;
  let prevY: number | null = null;
  let anim: Animation | null = null;
  let shown: boolean | null = null;
  let dotHeight: number | null = null;
  const dotHeightPx = (dot: HTMLElement): number => {
    if (dotHeight === null) dotHeight = measureDotHeightPx(dot);
    return dotHeight;
  };

  let rowCache: HTMLElement[] | null = null;
  const rowsIn = (list: HTMLElement): HTMLElement[] => {
    if (rowCache === null) {
      rowCache = [...list.querySelectorAll<HTMLElement>(`[${NAV_ROW_ATTR}]`)];
    }
    return rowCache;
  };

  const rowAt = (list: HTMLElement, index: number): HTMLElement | undefined =>
    rowsIn(list).find(
      (row) => Number(row.getAttribute(NAV_ROW_ATTR)) === index,
    );

  let animatePlacement = false;
  const placement: DotPlacement = {
    read: () => {
      if (isShellLayoutBusy()) return;
      const dot = dotRef;
      const list = dot?.parentElement;
      if (!dot || !list) return;
      const row = props.index < 0 ? undefined : rowAt(list, props.index);
      return { dot, y: row ? centerYInList(row, dotHeightPx(dot)) : null, animate: animatePlacement };
    },
    write: ({ dot, y, animate }) => {
      if (y === null) {
        anim?.cancel();
        anim = null;
        if (shown !== false) {
          shown = false;
          dot.style.opacity = "0";
        }
        prevY = null;
        return;
      }
      if (shown !== true) {
        shown = true;
        dot.style.opacity = "1";
      }
      if (prevY === y) return;

      anim?.cancel();
      anim = null;

      if (!animate || prevY === null || shouldSnapSelectionTravel(dot)) {
        dot.style.transform = `translateY(${y}px)`;
        prevY = y;
        return;
      }

      const from = prevY;
      perfMark("navdot:animate", { from: Math.round(from), to: Math.round(y) });
      dot.style.transform = `translateY(${from}px)`;
      anim = playSelectionTravel(
        dot,
        [
          { transform: `translateY(${from}px)` },
          { transform: `translateY(${y}px)` },
        ],
        () => {
          dot.style.transform = `translateY(${y}px)`;
          anim = null;
        },
      );
      prevY = y;
    },
  };
  const place = (animate: boolean) => {
    animatePlacement = animate;
    placements.request(placement);
  };

  createEffect(() => {
    void props.index;
    place(true);
  });

  createEffect(() => {
    const dot = dotRef;
    if (!dot) return;
    const list = dot.parentElement;
    if (!list || typeof ResizeObserver === "undefined") return;

    const ro = new ResizeObserver(() => schedule(false));
    // Resized rows above the marker also change its position.
    const observed = new Set<HTMLElement>([list]);
    ro.observe(list);
    // Only added rows need observation; re-observation triggers callbacks.
    const syncRows = () => {
      rowCache = null;
      const next = new Set<HTMLElement>([list, ...rowsIn(list)]);
      for (const row of observed) {
        if (!next.has(row)) {
          ro.unobserve(row);
          observed.delete(row);
        }
      }
      for (const row of next) {
        if (observed.has(row)) continue;
        ro.observe(row);
        observed.add(row);
      }
    };
    // Row changes share one measurement after shell layout settles.
    let frame = 0;
    let resync = false;
    const schedule = (needsResync: boolean) => {
      resync ||= needsResync;
      if (frame !== 0) return;
      frame = requestAnimationFrame(() => {
        frame = 0;
        if (resync) {
          resync = false;
          syncRows();
        }
        if (!isShellLayoutBusy()) place(false);
      });
    };
    syncRows();
    const mo =
      typeof MutationObserver === "undefined"
        ? null
        : new MutationObserver(() => schedule(true));
    mo?.observe(list, { childList: true });
    const stopSettle = onShellLayoutSettled(() => {
      dotHeight = null;
      place(false);
    });
    onCleanup(() => {
      if (frame !== 0) cancelAnimationFrame(frame);
      ro.disconnect();
      mo?.disconnect();
      stopSettle();
    });
  });

  onCleanup(() => {
    placements.cancel(placement);
    anim?.cancel();
  });

  return (
    <span class="den-nav-marker" aria-hidden="true" ref={(el) => (dotRef = el)} />
  );
}
