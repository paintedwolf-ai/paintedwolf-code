import { createSignal } from "solid-js";
import type { ActivityEvent, CommandProgramReceipt } from "../../api/types.ts";

const MAX_CALLS = 128;
const programs = new Map<string, CommandProgramReceipt>();
const [revision, setRevision] = createSignal(0);
const [visibilityRevision, setVisibilityRevision] = createSignal(0);

function hasActivities(program: CommandProgramReceipt | undefined): boolean {
  return (program?.activities?.length ?? 0) > 0;
}

export function hasLiveCommandActivities(sessionId: string, callId: string): boolean {
  visibilityRevision();
  return hasActivities(programs.get(`${sessionId}:${callId}`));
}

export function observeCommandActivity(event: ActivityEvent): void {
  const program = event.progress?.command_program;
  if (!event.tool_call_id || !program) return;
  const key = `${event.session_id}:${event.tool_call_id}`;
  const previous = programs.get(key);
  if (previous?.activity_revision !== undefined && program.activity_revision !== undefined && program.activity_revision < previous.activity_revision) return;
  programs.delete(key);
  programs.set(key, program);
  let visibilityChanged = hasActivities(previous) !== hasActivities(program);
  if (programs.size > MAX_CALLS) {
    const oldest = programs.keys().next().value!;
    visibilityChanged = hasActivities(programs.get(oldest)) || visibilityChanged;
    programs.delete(oldest);
  }
  if (visibilityChanged) setVisibilityRevision(value => value + 1);
  setRevision(value => value + 1);
}

/** Terminal snapshots bridge the live-event to canonical-result handoff. */
export function liveCommandProgram(sessionId: string | undefined, callId: string): CommandProgramReceipt | undefined {
  revision();
  return sessionId ? programs.get(`${sessionId}:${callId}`) : undefined;
}

export function clearCommandActivities(sessionId: string): void {
  let visibilityChanged = false;
  for (const [key, program] of programs) {
    if (!key.startsWith(`${sessionId}:`)) continue;
    visibilityChanged = hasActivities(program) || visibilityChanged;
    programs.delete(key);
  }
  if (visibilityChanged) setVisibilityRevision(value => value + 1);
  setRevision(value => value + 1);
}
