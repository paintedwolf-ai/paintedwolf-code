import type { Message, DraftVersion } from "../../../../api/types.ts";
import fixtureJson from "./fixture.json";

/** Immutable host ordinals determine order across arrival permutations. */
export type FixtureMessage = Message & {
  ord: number;
};

export type OrderableItem = {
  kind: string;
  key: string;
  ord: number;
  sub: number;
};

export type WorkerFileEditFixture = {
  key: string;
  job_id: string;
  parent_message_id: string;
  ord: number;
  sub: number;
  file_edit: { path: string; before?: string; after: string };
  pending: boolean;
  ts: string;
};

export type InlineCheckpointFixture = {
  key: string;
  tool_call_id: string;
  parent_message_id: string;
  ord: number;
  sub: number;
  kind: string;
  status: string;
};

export type OrderingFixture = {
  session_id: string;
  workflow_run_id: string;
  messages: FixtureMessage[];
  draft_versions: DraftVersion[];
  worker_file_edits: WorkerFileEditFixture[];
  inline_checkpoints: InlineCheckpointFixture[];
  canonical_order: OrderableItem[];
};

export function loadOrderingFixture(): OrderingFixture {
  return fixtureJson as unknown as OrderingFixture;
}

export type CanonicalOrderResult = {
  ok: boolean;
  diff: string | null;
};

/** Compare rendered identity and host order independently of arrival order. */
export function assertCanonicalOrder(
  actual: readonly OrderableItem[],
  expected: readonly OrderableItem[],
): CanonicalOrderResult {
  const sorted = [...actual].sort((a, b) => a.ord - b.ord || a.sub - b.sub);
  if (sorted.length !== expected.length) {
    return {
      ok: false,
      diff: `length: actual ${sorted.length} !== expected ${expected.length}`,
    };
  }
  for (let i = 0; i < sorted.length; i++) {
    const a = sorted[i]!;
    const e = expected[i]!;
    if (a.kind !== e.kind || a.key !== e.key || a.ord !== e.ord || a.sub !== e.sub) {
      return {
        ok: false,
        diff: `index ${i}: actual {kind:${a.kind},key:${a.key},ord:${a.ord},sub:${a.sub}} !== expected {kind:${e.kind},key:${e.key},ord:${e.ord},sub:${e.sub}}`,
      };
    }
  }
  return { ok: true, diff: null };
}
