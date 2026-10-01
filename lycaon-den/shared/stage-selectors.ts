/**
 * Selectors for the live chat stage, shared by the harness driver and the e2e helpers.
 * Visited chats stay mounted (hidden, inert) inside `.den-resident-surface[data-resident="idle"]`,
 * so a document-wide query for chat chrome can match an idle copy.
 */
export const CHAT_STAGE_SELECTOR = ".den-shell-stage--chat";

/** The chat stage currently receiving input. */
export const LIVE_CHAT_STAGE_SELECTOR = `.den-resident-surface:not([data-resident="idle"]) ${CHAT_STAGE_SELECTOR}`;
