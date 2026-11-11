import { createContext, onCleanup, useContext, type Accessor, type JSX } from "solid-js";
import type { ParticipantReport, Preparation } from "./presentation.ts";

const PresentationContext = createContext<Preparation>();

export function PresentationProvider(props: { preparation: Preparation; children: JSX.Element }) {
  return <PresentationContext.Provider value={props.preparation}>{props.children}</PresentationContext.Provider>;
}

/** Registers synchronously, before a parent can publish its first frame. */
export function usePresentationParticipant(
  name: string,
  ready: Accessor<boolean>,
  report?: ParticipantReport,
): void {
  const preparation = useContext(PresentationContext);
  if (preparation) onCleanup(preparation.register(name, ready, report));
}
