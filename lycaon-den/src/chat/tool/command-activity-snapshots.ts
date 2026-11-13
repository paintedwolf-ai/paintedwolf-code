import { createMemo, createResource } from "solid-js";
import type { CommandProgramReceipt } from "../../api/types.ts";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import type { ToolPartView } from "./tool-part-model.ts";
import { liveCommandProgram } from "./command-activity-live.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";

export function commandProgramActive(program: CommandProgramReceipt): boolean {
  return program.state === "running" || program.state === "waiting";
}

/** The newer of the persisted program and an observed one; ties go to the observation. */
export function preferredCommandProgram(canonical: CommandProgramReceipt | undefined, observed: CommandProgramReceipt | undefined): CommandProgramReceipt | undefined {
  if (!observed) return canonical;
  if (!canonical) return observed;
  return (observed.activity_revision ?? 0) >= (canonical.activity_revision ?? 0) ? observed : canonical;
}

/**
 * Retained transcripts resolve their activity projection before mounting its rows.
 * A persisted program needs the host's receipt unless a live event already
 * supersedes it or it settled with its steps recorded.
 */
export function useCommandActivitySnapshots(entries: () => readonly ActivitySpanEntry[], sessionId: () => string | undefined) {
  const requests = createMemo(() => {
    const session = sessionId();
    const client = getLycaonClient();
    if (!session || !client) return undefined;
    const programs = [...new Map(entries().flatMap(entry => {
      if (entry.kind !== "tool") return [];
      const program = entry.part.commandProgram;
      if (!program || liveCommandProgram(session, entry.part.toolCallId)) return [];
      if (!commandProgramActive(program) && program.activities?.some(fact => fact.steps !== undefined)) return [];
      return [[program.id, program] as const];
    })).values()];
    return programs.length ? { session, client, programs, key: programs.map(program => program.id).join(":") } : undefined;
  }, undefined, { equals: (a, b) => a?.session === b?.session && a?.client === b?.client && a?.key === b?.key });
  const [snapshots] = createResource(requests, async ({ session, client, programs }) => {
    const results = new Map<string, CommandProgramReceipt>();
    for (let offset = 0; offset < programs.length; offset += 2) {
      await Promise.all(programs.slice(offset, offset + 2).map(async program => {
        const page = await client.getCommandProgramReceipt(session, program.id, { limit: 1 });
        results.set(program.id, { ...page.program, activities: page.activities });
      }));
    }
    return results;
  });
  return (part: ToolPartView): CommandProgramReceipt | undefined => {
    const live = liveCommandProgram(sessionId(), part.toolCallId);
    if (live) return live;
    const program = part.commandProgram;
    if (!program) return undefined;
    if (snapshots.state === "errored") return { ...program, activities: undefined };
    const retained = snapshots.latest?.get(program.id);
    if (retained) return retained;
    if (!requests()?.programs.some(value => value.id === program.id)) return undefined;
    // An unsettled program keeps its recorded rows until the receipt arrives.
    return commandProgramActive(program) ? program : { ...program, activities: [] };
  };
}
