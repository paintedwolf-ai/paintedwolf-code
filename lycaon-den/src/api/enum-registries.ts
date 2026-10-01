/**
 * Wire-enum registries for Den invariant tests, parsed from generated `types.ts`.
 * Subset filters (e.g. resolved checkpoints) are predicates over the full registry.
 */
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type {
  CheckpointStatus,
  MessageKind,
  ApprovalGrantCategory,
  WorkerStatus,
  WorkerSummaryStatus,
  WorkflowBoundaryKind,
  WorkflowRunStatus,
} from "./types.ts";
import { parseTSEnumUnions } from "./openapi-sync.ts";

const typesPath = join(dirname(fileURLToPath(import.meta.url)), "types.ts");

let cachedEnums: Map<string, string[]> | undefined;

function loadWireEnums(): Map<string, string[]> {
  if (!cachedEnums) {
    cachedEnums = parseTSEnumUnions(readFileSync(typesPath, "utf8"));
  }
  return cachedEnums;
}

export function wireEnumValues<T extends string>(
  schemaName: (typeof WIRE_ENUM_REGISTRY_NAMES)[number],
): readonly T[] {
  const values = loadWireEnums().get(schemaName);
  if (!values?.length) {
    throw new Error(
      `wire enum ${schemaName} missing from generated types.ts — run ./task codegen:den-types`,
    );
  }
  return values as T[];
}

/** Every CheckpointStatus on the wire. */
export function allCheckpointStatuses(): readonly CheckpointStatus[] {
  return wireEnumValues<CheckpointStatus>("CheckpointStatus");
}

/** Every MessageKind on the wire. */
export function allMessageKinds(): readonly MessageKind[] {
  return wireEnumValues<MessageKind>("MessageKind");
}

/** Durable decision chicklets — pending is live UI, not action-completeness. */
export function resolvedCheckpointStatuses(): readonly Exclude<
  CheckpointStatus,
  "pending"
>[] {
  return allCheckpointStatuses().filter(
    (status): status is Exclude<CheckpointStatus, "pending"> =>
      status !== "pending",
  );
}

/** Every WorkerStatus on the wire (roster job lifecycle). */
export function allWorkerStatuses(): readonly WorkerStatus[] {
  return wireEnumValues<WorkerStatus>("WorkerStatus");
}

/** Every WorkerSummaryStatus on the wire (canonical task-row patch). */
export function allWorkerSummaryStatuses(): readonly WorkerSummaryStatus[] {
  return wireEnumValues<WorkerSummaryStatus>("WorkerSummaryStatus");
}

/** Every WorkflowRunStatus on the wire. */
export function allWorkflowRunStatuses(): readonly WorkflowRunStatus[] {
  return wireEnumValues<WorkflowRunStatus>("WorkflowRunStatus");
}

/** Every WorkflowBoundaryKind on the wire. */
export function allWorkflowBoundaryKinds(): readonly WorkflowBoundaryKind[] {
  return wireEnumValues<WorkflowBoundaryKind>("WorkflowBoundaryKind");
}

/** Every reusable lease category on the wire. */
export function allApprovalGrantCategories(): readonly ApprovalGrantCategory[] {
  return wireEnumValues<ApprovalGrantCategory>("ApprovalGrantCategory");
}

/** Schema names defined by this registry surface (self-updating coverage guard). */
export const WIRE_ENUM_REGISTRY_NAMES = [
  "CheckpointStatus",
  "MessageKind",
  "ApprovalGrantCategory",
  "WorkerStatus",
  "WorkerSummaryStatus",
  "WorkflowRunStatus",
  "WorkflowBoundaryKind",
] as const;
