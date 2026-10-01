import { createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ApprovalGrant, AskQuiet } from "../../api/types.ts";

let grants: ApprovalGrant[] = [];
let grantsById = new Map<string, ApprovalGrant>();
let quiets: AskQuiet[] = [];
let quietsById = new Map<string, AskQuiet>();
let loaded = false;
let loadErr: string | undefined;
let inflight: Promise<void> | null = null;
let clientRef: LycaonClient | null = null;
let generation = 0;

/** Subscription clock for membership and load-state changes. */
const [epoch, setEpoch] = createSignal(0);

function bump(): void {
  setEpoch((n) => n + 1);
}

/** Drop cached membership so readers refetch. */
export function invalidateApprovalGrantsCache(): void {
  generation += 1;
  grants = [];
  grantsById = new Map();
  quiets = [];
  quietsById = new Map();
  loaded = false;
  loadErr = undefined;
  inflight = null;
  bump();
}

/** Load the shared list once and coalesce concurrent callers. */
export async function loadApprovalGrantsCache(
  client: LycaonClient,
): Promise<void> {
  if (clientRef !== client) invalidateApprovalGrantsCache();
  clientRef = client;
  if (loaded) return;
  if (inflight) return inflight;
  const requestGeneration = generation;
  inflight = (async () => {
    try {
      const res = await client.listApprovalGrants();
      if (requestGeneration !== generation) return;
      grants = res.grants;
      grantsById = new Map(res.grants.map((g) => [g.id, g]));
      quiets = Array.isArray(res.quiets) ? res.quiets : Object.values(res.quiets ?? {});
      quietsById = new Map(quiets.map((q) => [q.id, q]));
      loaded = true;
      loadErr = undefined;
    } catch (err) {
      if (requestGeneration !== generation) return;
      loadErr = err instanceof Error ? err.message : String(err);
    } finally {
      if (requestGeneration === generation) bump();
    }
  })().finally(() => {
    if (requestGeneration === generation) inflight = null;
  });
  return inflight;
}

export function approvalGrants(): readonly ApprovalGrant[] {
  epoch();
  return grants;
}

export function approvalQuiets(): readonly AskQuiet[] {
  epoch();
  return quiets;
}

export function approvalGrantsError(): string | undefined {
  epoch();
  return loadErr;
}

/** Clear a load error so readers retry. */
export function retryApprovalGrantsLoad(): void {
  loadErr = undefined;
  bump();
}

export function isApprovalGrantsCacheLoaded(): boolean {
  epoch();
  return loaded;
}

export function hasGrant(id: string): boolean {
  epoch();
  return grantsById.has(id);
}

export function hasQuiet(id: string): boolean {
  epoch();
  return quietsById.has(id);
}

export function refreshApprovalGrantsCache(): void {
  const client = clientRef;
  if (!client) return;
  generation += 1;
  inflight = null;
  loaded = false;
  void loadApprovalGrantsCache(client).catch(() => undefined);
}

/** Remove revoked entries immediately, then refresh. */
export function revokeGrantsOptimistic(ids: readonly string[]): void {
  let changed = false;
  for (const id of ids) {
    if (grantsById.delete(id)) changed = true;
    if (quietsById.delete(id)) changed = true;
  }
  if (changed) {
    grants = grants.filter((g) => grantsById.has(g.id));
    quiets = quiets.filter((q) => quietsById.has(q.id));
    bump();
  }
  const client = clientRef;
  if (!client) return;
  generation += 1;
  inflight = null;
  loaded = false;
  void loadApprovalGrantsCache(client).catch(() => undefined);
}
