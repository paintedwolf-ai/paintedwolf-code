import { createSignal } from "solid-js";
import type { SourceOperationEvent, SourceOperationStatus } from "../api/types.ts";

/** The host's operation list keeps at most this many per project. */
const OPERATION_LIMIT = 100;

const [projects, setProjects] = createSignal<ReadonlyMap<string, readonly SourceOperationStatus[]>>(new Map());
const [resyncRevision, setResyncRevision] = createSignal(0);
let eventRevision = 0;
const eventVersions = new Map<string, Map<string, number>>();

/** Capture before reading a host snapshot so later events retain precedence. */
export function sourceOperationsEventRevision(): number {
  return eventRevision;
}

function retainEventVersions(projectId: string, operations: readonly SourceOperationStatus[]): void {
  const versions = eventVersions.get(projectId);
  if (!versions) return;
  const retained = new Set(operations.map((operation) => operation.operation_id));
  for (const id of versions.keys()) if (!retained.has(id)) versions.delete(id);
}

/** Active work by submission, then finished work by completion, newest first in each, as the host lists them. */
function ordered(requests: readonly SourceOperationStatus[]): SourceOperationStatus[] {
  const listedAt = (request: SourceOperationStatus) => Date.parse(request.completed_at ?? request.created_at);
  return [...requests]
    .sort((a, b) => {
      if (a.complete !== b.complete) return a.complete ? 1 : -1;
      const newer = listedAt(b) - listedAt(a);
      return newer !== 0 ? newer : a.operation_id < b.operation_id ? -1 : 1;
    })
    .slice(0, OPERATION_LIMIT);
}

/** File operations the host reports for a project, live from `source_operation` events. */
export function sourceOperationsFor(projectId: string): readonly SourceOperationStatus[] {
  return projects().get(projectId) ?? [];
}

/** Reconciles a host snapshot with events received since the read began. */
export function replaceSourceOperations(projectId: string, operations: readonly SourceOperationStatus[], snapshotRevision = eventRevision): void {
  setProjects((previous) => {
    const byId = new Map(operations.map((operation) => [operation.operation_id, operation]));
    const versions = eventVersions.get(projectId);
    for (const operation of previous.get(projectId) ?? []) {
      if ((versions?.get(operation.operation_id) ?? 0) > snapshotRevision) byId.set(operation.operation_id, operation);
    }
    const next = ordered([...byId.values()]);
    retainEventVersions(projectId, next);
    return new Map(previous).set(projectId, next);
  });
}

/** Applies one persisted request state; a request the list never held is added. */
export function applySourceOperationEvent(event: SourceOperationEvent): void {
  let versions = eventVersions.get(event.project_id);
  if (!versions) eventVersions.set(event.project_id, versions = new Map());
  versions.set(event.operation.operation_id, ++eventRevision);
  setProjects((previous) => {
    const current = previous.get(event.project_id) ?? [];
    const next = current.filter((request) => request.operation_id !== event.operation.operation_id);
    next.push(event.operation);
    const operations = ordered(next);
    retainEventVersions(event.project_id, operations);
    return new Map(previous).set(event.project_id, operations);
  });
}

/** A fresh event stream may have missed transitions; consumers re-list on this revision. */
export function resyncSourceOperations(): void {
  setResyncRevision((revision) => revision + 1);
}

export function sourceOperationsResyncRevision(): number {
  return resyncRevision();
}

export function resetSourceOperationsForTests(): void {
  eventRevision = 0;
  eventVersions.clear();
  setProjects(new Map());
  setResyncRevision(0);
}
