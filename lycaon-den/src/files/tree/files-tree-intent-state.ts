import type { DenFilesTreeIntent, DenFilesTreeIntentState } from "../../../shared/app-state-types.ts";
import type { SourceTreeDisclosure } from "../../api/types.ts";
import { clientIdentity } from "../../platform/connection/client-identity.ts";
import { appStateUnwritten, flushAppState, getAppStateSnapshot, persistAppState } from "../../store/app-state-snapshot.ts";

const WORKSPACE_CAP = 16;
const WINDOW_CAP = 8;
const DISCLOSURE_CAP = 2048;

function prune<T extends { touchedAt: number }>(values: Record<string, T>, cap: number): Record<string, T> {
  return Object.fromEntries(Object.entries(values).sort((a, b) => b[1].touchedAt - a[1].touchedAt).slice(0, cap));
}

export function restoredFilesTreeDisclosures(workspace: string): SourceTreeDisclosure[] | undefined {
  const saved = getAppStateSnapshot().filesTreeIntent?.byWindow[clientIdentity()]?.byWorkspace[workspace];
  return saved ? structuredClone(saved.disclosures) : undefined;
}

/** Command acceptance saves immutable intent before publishing its presentation. */
export async function saveFilesTreeIntent(workspace: string, revision: string, disclosures: readonly SourceTreeDisclosure[]): Promise<void> {
  const current = getAppStateSnapshot().filesTreeIntent?.byWindow ?? {};
  const window = clientIdentity();
  const previous = current[window]?.byWorkspace ?? {};
  if (previous[workspace]?.revision === revision || JSON.stringify(previous[workspace]?.disclosures) === JSON.stringify(disclosures)) {
    // An unchanged intent still waits for a write that failed or is in flight.
    if (appStateUnwritten("filesTreeIntent")) await flushAppState();
    return;
  }
  if (disclosures.length > DISCLOSURE_CAP) throw new Error("The saved tree configuration exceeds its limit.");
  const touchedAt = Date.now();
  const record: DenFilesTreeIntent = { revision, disclosures: structuredClone([...disclosures]), touchedAt };
  const byWorkspace = prune({ ...previous, [workspace]: record }, WORKSPACE_CAP);
  const byWindow = prune({ ...current, [window]: { byWorkspace, touchedAt } }, WINDOW_CAP);
  const saved = persistAppState({ filesTreeIntent: { byWindow } });
  await Promise.all([saved, flushAppState()]);
}

export function parseFilesTreeIntentState(value: unknown): DenFilesTreeIntentState | undefined {
  if (!value || typeof value !== "object" || !("byWindow" in value) || !value.byWindow || typeof value.byWindow !== "object") return;
  const byWindow: DenFilesTreeIntentState["byWindow"] = {};
  for (const [label, candidate] of Object.entries(value.byWindow)) {
    if (!label || !candidate || typeof candidate !== "object" || !("byWorkspace" in candidate) || !candidate.byWorkspace || typeof candidate.byWorkspace !== "object") continue;
    const byWorkspace: Record<string, DenFilesTreeIntent> = {};
    for (const [workspace, input] of Object.entries(candidate.byWorkspace)) {
      const parsed = parseIntent(input);
      if (workspace && parsed) byWorkspace[workspace] = parsed;
    }
    if (!Object.keys(byWorkspace).length) continue;
    byWindow[label] = { byWorkspace: prune(byWorkspace, WORKSPACE_CAP), touchedAt: Math.max(...Object.values(byWorkspace).map(item => item.touchedAt)) };
  }
  return Object.keys(byWindow).length ? { byWindow: prune(byWindow, WINDOW_CAP) } : undefined;
}

export function mergePeerTreeIntents(current: DenFilesTreeIntentState | undefined, incoming: unknown, identity: string): DenFilesTreeIntentState | undefined {
  const received = parseFilesTreeIntentState(incoming);
  const local = current?.byWindow[identity];
  if (!local) return received;
  return { byWindow: { ...received?.byWindow, [identity]: local } };
}

function parseIntent(value: unknown): DenFilesTreeIntent | undefined {
  if (!value || typeof value !== "object") return;
  const row = value as Partial<DenFilesTreeIntent>;
  if (typeof row.revision !== "string" || !row.revision || !Array.isArray(row.disclosures) || row.disclosures.length > DISCLOSURE_CAP) return;
  const disclosures: SourceTreeDisclosure[] = [];
  for (const disclosure of row.disclosures) {
    const address = disclosure?.address;
    if (!address || typeof address.root_id !== "string" || !address.root_id || typeof address.path !== "string" || !address.path ||
      address.path.includes("\\") || address.path.includes("\0") || typeof disclosure.open !== "boolean" || typeof disclosure.recursive !== "boolean") return;
    if (address.path !== "." && address.path.split("/").some(part => !part || part === "." || part === "..")) return;
    disclosures.push({ address: { ...address }, open: disclosure.open, recursive: disclosure.recursive });
  }
  return { revision: row.revision, disclosures, touchedAt: typeof row.touchedAt === "number" && Number.isFinite(row.touchedAt) ? row.touchedAt : 0 };
}
