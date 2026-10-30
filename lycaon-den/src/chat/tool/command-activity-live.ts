import { createSignal } from "solid-js";
import type { ActivityEvent, CommandProgramReceipt } from "../../api/types.ts";

const MAX_CALLS = 128;
const programs = new Map<string, CommandProgramReceipt>();
const [revision, setRevision] = createSignal(0);

export function observeCommandActivity(event: ActivityEvent): void {
  const program = event.progress?.command_program;
  if (!event.tool_call_id || !program) return;
  const key = `${event.session_id}:${event.tool_call_id}`;
  programs.delete(key);
  programs.set(key, program);
  while (programs.size > MAX_CALLS) programs.delete(programs.keys().next().value!);
  setRevision(value => value + 1);
}

/** Terminal snapshots bridge the live-event to canonical-result handoff. */
export function liveCommandProgram(sessionId: string | undefined, callId: string): CommandProgramReceipt | undefined {
  revision();
  return sessionId ? programs.get(`${sessionId}:${callId}`) : undefined;
}

export function clearCommandActivities(sessionId: string): void {
  for (const key of programs.keys()) if (key.startsWith(`${sessionId}:`)) programs.delete(key);
  setRevision(value => value + 1);
}
