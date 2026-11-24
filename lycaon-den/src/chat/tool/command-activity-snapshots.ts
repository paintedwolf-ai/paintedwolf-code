import { createMemo, createResource } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { CommandProgramReceipt } from "../../api/types.ts";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import type { ToolPartView } from "./tool-part-model.ts";
import { liveCommandProgram } from "./command-activity-live.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { ByteCache, retainedValueBytes } from "../../utils/byte-cache.ts";

export const COMMAND_ACTIVITY_SUMMARY_CACHE_MAX_ENTRIES = 32;
export const COMMAND_ACTIVITY_SUMMARY_CACHE_MAX_BYTES = 512 * 1024;

type ClientSnapshotCache = {
  inFlight: Map<string, Promise<CommandProgramReceipt>>;
  settled: ByteCache<string, CommandProgramReceipt>;
};

let clientCaches = new WeakMap<LycaonClient, ClientSnapshotCache>();

function cacheForClient(client: LycaonClient): ClientSnapshotCache {
  let cache = clientCaches.get(client);
  if (!cache) {
    cache = {
      inFlight: new Map(),
      settled: new ByteCache(
        COMMAND_ACTIVITY_SUMMARY_CACHE_MAX_BYTES,
        COMMAND_ACTIVITY_SUMMARY_CACHE_MAX_ENTRIES,
      ),
    };
    clientCaches.set(client, cache);
  }
  return cache;
}

export function clearCommandActivitySnapshotCache(): void {
  clientCaches = new WeakMap();
}

function summaryCacheKey(sessionId: string, programId: string, revision: number | undefined): string {
  return `${sessionId}\0${programId}\0${revision ?? "none"}`;
}

function fetchSharedSummary(
  client: LycaonClient,
  session: string,
  program: CommandProgramReceipt,
): Promise<CommandProgramReceipt> {
  const cache = cacheForClient(client);
  const key = summaryCacheKey(session, program.id, program.activity_revision);
  const warm = cache.settled.get(key);
  if (warm) return Promise.resolve(warm);
  const pending = cache.inFlight.get(key);
  if (pending) return pending;

  const request = client
    .getCommandProgramReceipt(session, program.id, { limit: 1 })
    .then((page) => {
      const receipt: CommandProgramReceipt = { ...page.program, activities: page.activities };
      const requestedRev = program.activity_revision;
      const returnedRev = receipt.activity_revision;
      const staleForRequest =
        requestedRev !== undefined && (returnedRev ?? 0) < requestedRev;
      if (!commandProgramActive(receipt) && !staleForRequest) {
        const bytes = retainedValueBytes(receipt) + key.length * 2;
        cache.settled.set(key, receipt, bytes);
        if (returnedRev !== undefined && returnedRev !== requestedRev) {
          const revKey = summaryCacheKey(session, program.id, returnedRev);
          cache.settled.set(revKey, receipt, retainedValueBytes(receipt) + revKey.length * 2);
        }
      }
      cache.inFlight.delete(key);
      return receipt;
    })
    .catch((err) => {
      cache.inFlight.delete(key);
      throw err;
    });

  cache.inFlight.set(key, request);
  return request;
}

export function commandProgramActive(program: CommandProgramReceipt): boolean {
  return program.state === "running" || program.state === "waiting";
}

/** The newer of the persisted program and an observed one; ties go to the observation. */
export function preferredCommandProgram(canonical: CommandProgramReceipt | null | undefined, observed: CommandProgramReceipt | undefined): CommandProgramReceipt | undefined {
  if (!observed) return canonical ?? undefined;
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
    const cache = cacheForClient(client);
    const programs = [...new Map(entries().flatMap(entry => {
      if (entry.kind !== "tool") return [];
      const program = entry.part.commandProgram;
      if (!program || liveCommandProgram(session, entry.part.toolCallId)) return [];
      if (!commandProgramActive(program) && program.activities?.some(fact => fact.steps !== undefined)) return [];
      const key = summaryCacheKey(session, program.id, program.activity_revision);
      if (cache.settled.get(key)) return [];
      return [[`${program.id}\0${program.activity_revision ?? "none"}`, program] as const];
    })).values()];
    return programs.length
      ? { session, client, programs, key: programs.map(p => `${p.id}\0${p.activity_revision ?? "none"}`).join(":") }
      : undefined;
  }, undefined, { equals: (a, b) => a?.session === b?.session && a?.client === b?.client && a?.key === b?.key });
  const [snapshots] = createResource(requests, async ({ session, client, programs }) => {
    const results = new Map<string, CommandProgramReceipt>();
    for (let offset = 0; offset < programs.length; offset += 2) {
      await Promise.all(programs.slice(offset, offset + 2).map(async program => {
        const receipt = await fetchSharedSummary(client, session, program);
        results.set(program.id, receipt);
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
    if (session && client) {
      const warm = cacheForClient(client).settled.get(
        summaryCacheKey(session, program.id, program.activity_revision),
      );
      if (warm) return warm;
    }
    if (snapshots.state === "errored") return { ...program, activities: undefined };
    const retained = snapshots.latest?.get(program.id);
    if (retained) {
      if (program.activity_revision !== undefined && (retained.activity_revision ?? 0) < program.activity_revision) {
        return { ...program, activities: program.activities };
      }
      return retained;
    }
    if (!requests()?.programs.some(value => value.id === program.id)) return undefined;
    // An unsettled program keeps its recorded rows until the receipt arrives.
    return commandProgramActive(program) ? program : { ...program, activities: [] };
  };
}
