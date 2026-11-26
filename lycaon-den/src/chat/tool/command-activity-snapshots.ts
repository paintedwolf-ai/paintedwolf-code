import { createMemo, createResource } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { CommandProgramReceipt } from "../../api/types.ts";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import type { ToolPartView } from "./tool-part-model.ts";
import { liveCommandProgram } from "./command-activity-live.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";

const MAX_CACHED_SNAPSHOTS = 256;
const settledSnapshots = new WeakMap<LycaonClient, Map<string, CommandProgramReceipt>>();

export function commandProgramActive(program: CommandProgramReceipt): boolean {
  return program.state === "running" || program.state === "waiting";
}

/** The newer of the persisted program and an observed one; ties go to the observation. */
export function preferredCommandProgram(canonical: CommandProgramReceipt | null | undefined, observed: CommandProgramReceipt | undefined): CommandProgramReceipt | undefined {
  if (!observed) return canonical ?? undefined;
  if (!canonical) return observed;
  return (observed.activity_revision ?? 0) >= (canonical.activity_revision ?? 0) ? observed : canonical;
}

function cacheKey(session: string, programId: string): string {
  return `${session}:${programId}`;
}

function cachedSnapshot(client: LycaonClient, session: string, program: CommandProgramReceipt): CommandProgramReceipt | undefined {
  const map = settledSnapshots.get(client);
  if (!map) return undefined;
  const key = cacheKey(session, program.id);
  const hit = map.get(key);
  if (!hit) return undefined;
  if ((hit.activity_revision ?? 0) < (program.activity_revision ?? 0)) {
    map.delete(key);
    return undefined;
  }
  map.delete(key);
  map.set(key, hit);
  return hit;
}

function storeSnapshot(client: LycaonClient, session: string, snapshot: CommandProgramReceipt): void {
  const key = cacheKey(session, snapshot.id);
  let map = settledSnapshots.get(client);
  if (commandProgramActive(snapshot)) {
    map?.delete(key);
    return;
  }
  if (!map) {
    map = new Map();
    settledSnapshots.set(client, map);
  }
  map.delete(key);
  map.set(key, snapshot);
  while (map.size > MAX_CACHED_SNAPSHOTS) {
    const oldest = map.keys().next().value;
    if (oldest === undefined) break;
    map.delete(oldest);
  }
}

/**
 * Retained transcripts resolve their activity projection before mounting its rows.
 * A persisted program needs the host's receipt unless a live event already
 * supersedes it, a settled snapshot is cached, or it settled with its steps recorded.
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
      if (cachedSnapshot(client, session, program)) return [];
      return [[program.id, program] as const];
    })).values()];
    return programs.length ? { session, client, programs, key: programs.map(program => program.id).join(":") } : undefined;
  }, undefined, { equals: (a, b) => a?.session === b?.session && a?.client === b?.client && a?.key === b?.key });
  const [snapshots] = createResource(requests, async ({ session, client, programs }) => {
    const results = new Map<string, CommandProgramReceipt>();
    for (let offset = 0; offset < programs.length; offset += 2) {
      await Promise.all(programs.slice(offset, offset + 2).map(async program => {
        const hit = cachedSnapshot(client, session, program);
        if (hit) {
          results.set(program.id, hit);
          return;
        }
        const page = await client.getCommandProgramReceipt(session, program.id, { limit: 1 });
        const snapshot: CommandProgramReceipt = { ...page.program, activities: page.activities };
        storeSnapshot(client, session, snapshot);
        results.set(program.id, snapshot);
      }));
    }
    return results;
  });
  return (part: ToolPartView): CommandProgramReceipt | undefined => {
    const session = sessionId();
    const live = liveCommandProgram(session, part.toolCallId);
    if (live) return live;
    const program = part.commandProgram;
    if (!program) return undefined;
    const client = getLycaonClient();
    const cached = session && client ? cachedSnapshot(client, session, program) : undefined;
    if (cached) return cached;
    if (snapshots.state === "errored") return { ...program, activities: undefined };
    const retained = snapshots.latest?.get(program.id);
    if (retained) return retained;
    if (!requests()?.programs.some(value => value.id === program.id)) return undefined;
    // An unsettled program keeps its recorded rows until the receipt arrives.
    return commandProgramActive(program) ? program : { ...program, activities: [] };
  };
}
