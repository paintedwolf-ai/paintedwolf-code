/** Composer-pad icon when Session.untrusted_content is true. */
export const EXTERNAL_CONTENT_BADGE_LABEL = "External content";

/** Hover copy — short; click opens session external-content search. */
export const EXTERNAL_CONTENT_BADGE_TITLE =
  "External content in this session — click to search";


/**
 * Global-search seed for external-content triggers in a session
 * (`untrusted:true` — web, MCP, and inherit/worker ledger markers).
 */
export function externalContentSearchQuery(sessionId: string): string {
  const id = sessionId.trim();
  if (!id) return "";
  return `session:${id} untrusted:true`;
}
