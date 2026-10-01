import type {
  LegStatus,
  SecurityFullPass,
  SecurityOverview,
  WorkflowExplainMeta,
  WorkflowRun,
  WorkflowTopologyLeg,
} from "../api/types.ts";
import type { ProgressRow } from "../components/primitives/ProgressRows.tsx";
import { fullPassDoneMembers } from "../lib/scan-coverage.ts";

export type WorkflowExplainStatus = "running" | "done" | "error";

/** The note's dot follows its run: running while the run holds the note's phase. */
export function workflowExplainStatus(
  meta: WorkflowExplainMeta,
  run: WorkflowRun | undefined,
): WorkflowExplainStatus {
  if (!run || run.current_phase !== meta.phase_id) return "done";
  if (run.status === "running") return "running";
  if (run.status === "failed") return "error";
  return "done";
}

/**
 * The pass the note names, as its folder's overview shows it now: live while
 * it runs, final once it is the latest full pass. A newer pass replaces it, and
 * the note stops drawing progress it can no longer read.
 */
export function workflowExplainPass(
  meta: WorkflowExplainMeta,
  overview: SecurityOverview | null | undefined,
): SecurityFullPass | null {
  const id = meta.progress?.full_pass?.assessment_id;
  if (!id || !overview) return null;
  if (overview.running?.assessment_id === id) return overview.running;
  if (overview.last_full?.assessment_id === id) return overview.last_full;
  return null;
}

/** Whether the note should keep its pass fresh: the run still holds its phase. */
export function workflowExplainWantsLiveProgress(
  meta: WorkflowExplainMeta,
  run: WorkflowRun | undefined,
): boolean {
  return meta.progress?.full_pass != null && workflowExplainStatus(meta, run) === "running";
}

/** The legs of the stages the note names, from the run's projection, in topology order. */
export function workflowExplainLegs(
  meta: WorkflowExplainMeta,
  run: WorkflowRun | undefined,
): WorkflowTopologyLeg[] {
  const stages = new Set(meta.progress?.topology?.stages ?? []);
  if (stages.size === 0) return [];
  return (run?.ui?.topology_legs ?? []).filter((leg) => stages.has(leg.stage));
}

const LEG_SETTLED: ReadonlySet<LegStatus> = new Set(["complete", "failed", "canceled"]);

const LEG_STATUS: Record<LegStatus, string> = {
  pending: "Waiting",
  dispatched: "Starting",
  running: "Running",
  retry_pending: "Retrying",
  held: "Needs a decision",
  complete: "Complete",
  failed: "Failed",
  canceled: "Canceled",
};

const legList = new Intl.ListFormat("en", { style: "long", type: "conjunction" });

function legReadout(leg: WorkflowTopologyLeg, byId: ReadonlyMap<string, WorkflowTopologyLeg>): string | undefined {
  switch (leg.status) {
    case "pending": {
      const waiting = (leg.waits_for ?? [])
        .map((id) => byId.get(id))
        .filter((pred): pred is WorkflowTopologyLeg => pred != null && !LEG_SETTLED.has(pred.status))
        .map((pred) => pred.label);
      return waiting.length > 0 ? `Starts after ${legList.format(waiting)}` : "Not started yet";
    }
    case "dispatched":
      return "A worker is picking this up";
    case "retry_pending":
      return "Continuing with a larger budget";
    case "held":
      return "Waiting on a decision";
    case "running":
    case "complete":
    case "failed":
    case "canceled":
      return undefined;
  }
}

/** Progress rows for legs; a leg's input legs may belong to another phase of the run. */
export function topologyLegRows(
  legs: readonly WorkflowTopologyLeg[],
  run: WorkflowRun | undefined,
): ProgressRow[] {
  const byId = new Map((run?.ui?.topology_legs ?? []).map((leg) => [leg.id, leg]));
  return legs.map((leg) => ({
    key: leg.id,
    label: leg.label,
    status: LEG_STATUS[leg.status],
    ratio: leg.status === "complete" ? 1 : 0,
    measured: leg.status === "complete",
    readout: legReadout(leg, byId),
    state: leg.status,
  }));
}

/** The collapsed row's state: how many scanners and workers have finished. */
export function workflowExplainState(pass: SecurityFullPass | null, legs: readonly WorkflowTopologyLeg[]): string {
  const parts: string[] = [];
  const scanners = pass?.members.length ?? 0;
  if (pass && scanners > 0) parts.push(`${fullPassDoneMembers(pass)} of ${scanners} scanners done`);
  if (legs.length > 0) {
    const done = legs.filter((leg) => LEG_SETTLED.has(leg.status)).length;
    parts.push(`${done} of ${legs.length} workers done`);
  }
  return parts.join(" · ");
}
