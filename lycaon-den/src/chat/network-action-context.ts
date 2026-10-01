import { createContext, useContext } from "solid-js";

/** ChatView provides host actions to nested network cards through context. */
export type NetworkActions = {
  /** Block a host going forward by writing a global `host` deny rule. */
  blockHost: (host: string) => Promise<void>;
  /** Whether this host has been blocked in this session (optimistic, for immediate feedback). */
  isBlocked: (host: string) => boolean;
};

export const NetworkActionContext = createContext<NetworkActions>();

export const useNetworkActions = () => useContext(NetworkActionContext);
