import { FrameMeasurements } from "../../layout/frame-measurements.ts";
import {
  playSelectionTravel,
  shouldSnapSelectionTravel,
} from "../../ui/selection-travel.ts";

const TRAVEL_KEY_ATTR = "data-travel-key";

/** Marks a row whose moves inside its list should travel rather than jump. */
export function travelKey(key: string): Record<string, string> {
  return { [TRAVEL_KEY_ATTR]: key };
}

type Offsets = Map<string, number>;
type Traveller = { list: () => HTMLElement | undefined; last: Offsets; skipNext: boolean };

function readOffsets(list: HTMLElement): Offsets {
  const offsets: Offsets = new Map();
  for (const row of list.querySelectorAll<HTMLElement>(`:scope > [${TRAVEL_KEY_ATTR}]`)) {
    const key = row.getAttribute(TRAVEL_KEY_ATTR);
    if (key !== null) offsets.set(key, row.offsetTop);
  }
  return offsets;
}

const travellers = new FrameMeasurements(
  (traveller: Traveller) => {
    const list = traveller.list();
    return list ? { list, offsets: readOffsets(list) } : undefined;
  },
  (traveller, measured) => {
    if (!measured) return;
    const { list, offsets } = measured;
    const previous = traveller.last;
    traveller.last = offsets;
    if (traveller.skipNext) {
      traveller.skipNext = false;
      return;
    }
    for (const [key, top] of offsets) {
      const was = previous.get(key);
      if (was === undefined || was === top) continue;
      const row = list.querySelector<HTMLElement>(`:scope > [${TRAVEL_KEY_ATTR}="${CSS.escape(key)}"]`);
      if (!row || shouldSnapSelectionTravel(row)) continue;
      playSelectionTravel(
        row,
        [{ transform: `translateY(${was - top}px)` }, { transform: "translateY(0)" }],
        () => {},
      );
    }
  },
);

/**
 * Rows that change places in a list travel from their old offset to the new
 * one. Offsets are list-relative, so content above the list does not count as
 * a move. Call `moved` after the order changes; `settle` records the current
 * places without animating, for a move the pointer already showed.
 */
export function createRowTravel(list: () => HTMLElement | undefined) {
  const traveller: Traveller = { list, last: new Map(), skipNext: false };
  return {
    moved: () => travellers.request(traveller),
    settle: () => {
      traveller.skipNext = true;
      travellers.request(traveller);
    },
    dispose: () => travellers.cancel(traveller),
  };
}
