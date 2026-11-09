import type { EditorDocumentChange } from "../../api/types.ts";
import type { FileHistoryRow } from "./file-history-model.ts";

type EditGroup = { kind: "edits"; changes: EditorDocumentChange[] };
type TimelineRow = FileHistoryRow | EditGroup;

function turnKey(sessionId: string | undefined, turn: number): string | undefined {
  return sessionId && turn > 0 ? JSON.stringify([sessionId, turn]) : undefined;
}

export function documentHistoryTimeline(rows: readonly FileHistoryRow[], changes: readonly EditorDocumentChange[]) {
  const groups: EditGroup[] = [];
  for (const change of changes) {
    const previous = groups.at(-1)?.changes;
    const head = previous?.[0];
    const key = change.actor_kind === "agent" && turnKey(change.session_id, change.turn);
    if (previous && head?.actor_kind === "agent" && head.epoch === change.epoch &&
        key && key === turnKey(head.session_id, head.turn)) {
      previous.push(change);
    } else {
      groups.push({ kind: "edits", changes: [change] });
    }
  }

  const versionsByTurn = new Map<string, string | null>();
  for (const row of rows) {
    if (row.kind !== "version" || row.version.origin !== "agent") continue;
    const key = turnKey(row.version.session_id, row.version.turn);
    if (key) versionsByTurn.set(key, versionsByTurn.has(key) ? null : row.version.id);
  }

  const editsByVersion = new Map<string, EditGroup[]>();
  const standalone: EditGroup[] = [];
  for (const group of groups) {
    const change = group.changes[0]!;
    const key = change.actor_kind === "agent" && turnKey(change.session_id, change.turn);
    const versionId = key && versionsByTurn.get(key);
    if (versionId) {
      const existing = editsByVersion.get(versionId);
      if (existing) existing.push(group);
      else editsByVersion.set(versionId, [group]);
    } else {
      standalone.push(group);
    }
  }

  const timeline: TimelineRow[] = [];
  let index = 0;
  for (const row of rows) {
    // Preserve ledger and Git lineage order. Compare only host observation times.
    const observedAt = row.kind === "version" ? row.version.created_at
      : row.kind === "arrival" ? row.change.observed_at : undefined;
    while (index < standalone.length && (!observedAt ||
      Date.parse(standalone[index]!.changes[0]!.created_at) >= Date.parse(observedAt))) {
      timeline.push(standalone[index++]!);
    }
    timeline.push(row);
    if (row.kind === "version") timeline.push(...editsByVersion.get(row.version.id) ?? []);
  }
  timeline.push(...standalone.slice(index));
  return timeline;
}
