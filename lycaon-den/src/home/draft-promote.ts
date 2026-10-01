/** Determines when a draft project offers Save to folder. */
export type DraftProjectPromoteInput = {
  isDraft: boolean;
  projectHasExchange: boolean;
  dismissed: boolean;
  /** True while a durable save transition is active. */
  promotePending: boolean;
  saveActionVisible: boolean;
};

export type DraftPromoteTurnInput = {
  messages: ReadonlyArray<{ role: string }>;
  /** True while the first turn is active. */
  live: boolean;
};

export function draftPromoteFirstTurnComplete(input: DraftPromoteTurnInput): boolean {
  if (input.live) return false;
  const hasUser = input.messages.some((m) => m.role === "user");
  const hasAssistant = input.messages.some((m) => m.role === "assistant");
  return hasUser && hasAssistant;
}

export function shouldShowDraftPromoteBanner(input: DraftProjectPromoteInput): boolean {
  if (input.promotePending || input.saveActionVisible) return false;
  return input.isDraft && input.projectHasExchange && !input.dismissed;
}

export function rememberDraftProjectExchange(
  known: ReadonlySet<string>,
  projectId: string,
): ReadonlySet<string> {
  if (known.has(projectId)) return known;
  return new Set(known).add(projectId);
}
