import { createContext, createMemo, useContext, type Accessor, type JSX } from "solid-js";
import type { Message, TurnLoad } from "../../api/types.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { buildTurnLoadIndex, toolLoadFact, type ToolLoadFact, type TurnLoadIndex } from "../../chat/turnload/turn-load-facts.ts";
import { sameWireValue } from "../../chat/transcript/projection/wire-equal.ts";

const TurnLoadIndexContext = createContext<Accessor<TurnLoadIndex>>();

/** Gives every tool card in the transcript the decision receipts of its turn. */
export function TurnLoadIndexProvider(props: {
  messages: Accessor<readonly Message[]>;
  turnLoads: Accessor<Readonly<Record<string, readonly TurnLoad[]>> | undefined>;
  children: JSX.Element;
}) {
  const index = createMemo(() => buildTurnLoadIndex(props.messages(), props.turnLoads()));
  return <TurnLoadIndexContext.Provider value={index}>{props.children}</TurnLoadIndexContext.Provider>;
}

/** The load fact for one card, or nothing outside a provided transcript. */
export function useToolLoadFact(part: () => ToolPartView): () => ToolLoadFact | undefined {
  const index = useContext(TurnLoadIndexContext);
  if (!index) return () => undefined;
  // The index is rebuilt per update; a card re-renders only when its own fact changes.
  return createMemo(() => toolLoadFact(index(), part()), undefined, { equals: sameWireValue });
}
