import { createEffect, createMemo, createSignal, on, type Accessor } from "solid-js";

export type StageTabRailFacts = {
  /** Session the store holds. */
  presentedSessionId: string | null;
  /** Session whose chat view serves the tab actions. */
  boundSessionId: string | null;
  /** The conversation, with its own tab rail, is legible in this window. */
  conversationShown: boolean;
  /** A project stage is legible in the stage column. */
  projectStageShown: boolean;
};

/** The session whose tabs the stage title bar carries while its conversation is off screen. */
export function stageTabRailSession(facts: StageTabRailFacts): string | null {
  if (facts.conversationShown || !facts.projectStageShown) return null;
  const sessionId = facts.presentedSessionId;
  return sessionId && sessionId === facts.boundSessionId ? sessionId : null;
}

export type StageTabRail<Tab> = {
  sessionId: Accessor<string | null>;
  openTab: Accessor<Tab | null>;
  selectTab: (next: Tab | null) => void;
};

/** A panel opened over one stage does not follow the rail to another session or stage. */
export function createStageTabRail<Tab>(
  facts: Accessor<StageTabRailFacts>,
  stageKey: Accessor<string | null>,
): StageTabRail<Tab> {
  const sessionId = createMemo(() => stageTabRailSession(facts()));
  const [openTab, selectTab] = createSignal<Tab | null>(null);
  createEffect(on([sessionId, stageKey], () => selectTab(null), { defer: true }));
  return { sessionId, openTab, selectTab };
}
