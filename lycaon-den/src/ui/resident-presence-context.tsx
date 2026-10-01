import {
  createContext,
  createMemo,
  useContext,
  type Accessor,
  type JSX,
} from "solid-js";
import type { ResidentPresence } from "./resident-surfaces.ts";

const ResidentPresenceContext = createContext<{
  presence: Accessor<ResidentPresence>;
  interactive: Accessor<boolean>;
}>({ presence: (): ResidentPresence => "active", interactive: () => true });

export function ResidentPresenceProvider(props: {
  presence: ResidentPresence;
  interactive?: boolean;
  children: JSX.Element;
}) {
  const parent = useContext(ResidentPresenceContext);
  // Memoized presence remains readable during child cleanup.
  const presence = createMemo(() => props.presence);
  const interactive = createMemo(() =>
    parent.interactive() && presence() === "active" && props.interactive !== false);
  return (
    <ResidentPresenceContext.Provider value={{ presence, interactive }}>
      {props.children}
    </ResidentPresenceContext.Provider>
  );
}

/** Standalone trees are active. */
export function useResidentPresence(): Accessor<ResidentPresence> {
  return useContext(ResidentPresenceContext).presence;
}

/** Retained and preparing surfaces cannot claim input, including through portals. */
export function useResidentInteractive(): Accessor<boolean> {
  return useContext(ResidentPresenceContext).interactive;
}
