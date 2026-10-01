import { createEffect, createSignal } from "solid-js";
import { widenWindowBy } from "../platform/windows/window-chrome.ts";

export type WindowClaim = {
  /** Asks the window for the width the pane needs. */
  claim: () => void;
  /** Refusals since the window last changed size. */
  refusals: () => number;
};

/** A width claim a fit-collapse control makes; a size change re-arms it. */
export function createWindowClaim(deps: {
  deficitPx: () => number;
  /** Read for its changes; any size change re-arms the claim. */
  rearmOn: () => unknown;
}): WindowClaim {
  const [refusals, setRefusals] = createSignal(0);

  createEffect(() => {
    deps.rearmOn();
    setRefusals(0);
  });

  return {
    claim: () => {
      void widenWindowBy(deps.deficitPx()).then((outcome) => {
        if (outcome === "no-room") setRefusals((count) => count + 1);
      });
    },
    refusals,
  };
}
