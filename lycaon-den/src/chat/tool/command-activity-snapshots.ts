import { createMemo, createResource } from "solid-js";
import type { CommandProgramReceipt } from "../../api/types.ts";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import type { ToolPartView } from "./tool-part-model.ts";
import { liveCommandProgram } from "./command-activity-live.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";

/** Retained transcripts resolve their activity projection before mounting its rows. */
export function useCommandActivitySnapshots(entries: () => readonly ActivitySpanEntry[], sessionId: () => string | undefined) {
  const requests = createMemo(() => {
    const session = sessionId();
    const client = getLycaonClient();
    if (!session || !client) return undefined;
    const programs = [...new Map(entries().flatMap(entry => {
      if (entry.kind !== "tool") return [];
      const program = entry.part.commandProgram;
      if (!program || program.state === "running" || program.state === "waiting" || program.activities?.some(fact => fact.steps !== undefined)) return [];
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
    return requests()?.programs.some(value => value.id === program.id) ? { ...program, activities: [] } : undefined;
  };
}
