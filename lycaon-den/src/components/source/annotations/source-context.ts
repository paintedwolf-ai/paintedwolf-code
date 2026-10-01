import { createContext, useContext } from "solid-js";
import type { ResolveProjectRoot } from "../../../api/project-path.ts";

/** Supplies project and overlay identity to rendered source links. */
export type SourceContextValue = {
  projectId: string;
  /** Roots with wire ids — Reveal and Add to chat need real root identity. */
  rootRefs?: readonly ResolveProjectRoot[];
  /** Write worker whose overlay holds these paths until promote. */
  jobId?: string;
};

const SourceContext = createContext<SourceContextValue>();

export const SourceContextProvider = SourceContext.Provider;

export function useSourceContext(): SourceContextValue | undefined {
  return useContext(SourceContext);
}
