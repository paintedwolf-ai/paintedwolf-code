export const FORM_PROGRAM_TOOLS: ReadonlySet<string> = new Set([
  "command",
  "summarize",
  "survey_repo",
  "code_rewrite",
]);

export const PROGRAM_ACTIVITY_TOOLS: ReadonlySet<string> = new Set([
  ...FORM_PROGRAM_TOOLS,
  "verify",
]);

const MAX_SESSIONS = 8;
const MAX_CALLS_PER_SESSION = 2048;
const shown = new Map<string, Set<string>>();

export function noteToolCallShownInFlight(sessionId: string, callId: string): void {
  const calls = shown.get(sessionId) ?? new Set<string>();
  shown.delete(sessionId);
  shown.set(sessionId, calls);
  calls.delete(callId);
  calls.add(callId);
  if (calls.size > MAX_CALLS_PER_SESSION) calls.delete(calls.values().next().value!);
  if (shown.size > MAX_SESSIONS) shown.delete(shown.keys().next().value!);
}

export function wasToolCallShownInFlight(sessionId: string, callId: string): boolean {
  if (!shown.get(sessionId)?.has(callId)) return false;
  noteToolCallShownInFlight(sessionId, callId);
  return true;
}
