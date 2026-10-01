/** Reports unresolved editor aims with the work responsible for their delay. */
import { denScrollDebugLog } from "../../chat/stream/den-scroll-debug.ts";
import type { FileBufferKey } from "./project-files-model.ts";

export type FilesPresentationLivenessFacts = {
  projectId: string;
  /** The buffer the reader aims at while another stays painted. */
  requestedKey: FileBufferKey | null;
  displayedKey: FileBufferKey | null;
  presentationPending: boolean;
  preparing: readonly string[];
  elapsedMs: number;
  /** A source read for the pending buffer has begun and not finished. */
  readInFlight: boolean;
  /** Project reads wait on the workspace, which has not settled. */
  workspaceResolving: boolean;
};

export type FilesPresentationLivenessViolation = {
  projectId: string;
  key: FileBufferKey;
  reason: string;
  preparing?: readonly string[];
  displayedKey?: FileBufferKey | null;
  elapsedMs?: number;
};

export function filesPresentationLivenessViolation(
  facts: FilesPresentationLivenessFacts,
): FilesPresentationLivenessViolation | null {
  if (!facts.requestedKey || !facts.presentationPending) return null;
  const preparing = [
    ...(facts.readInFlight ? ["source read"] : []),
    ...(facts.workspaceResolving ? ["workspace"] : []),
    ...facts.preparing,
  ];
  if (facts.elapsedMs < 300 || preparing.length && facts.elapsedMs < 10_000) return null;
  return {
    projectId: facts.projectId,
    key: facts.requestedKey,
    reason: preparing.length ? "presentation still pending after 10 seconds" : "pending presentation with no read in flight or preparation holder",
    preparing, displayedKey: facts.displayedKey, elapsedMs: facts.elapsedMs,
  };
}

const recorded: FilesPresentationLivenessViolation[] = [];

/** Surfaces a stuck aim; tests collect it, everything else logs it. */
export function reportFilesPresentationLivenessViolation(
  violation: FilesPresentationLivenessViolation,
): void {
  denScrollDebugLog("perf", "files-presentation-wait", {
    project: violation.projectId, requested: violation.key, displayed: violation.displayedKey ?? undefined,
    elapsed_ms: violation.elapsedMs, preparing: violation.preparing?.join(","), reason: violation.reason,
  });
  console.error(
    `Files presentation liveness: ${violation.reason} (project ${violation.projectId}, buffer ${violation.key})`, violation.preparing,
  );
  if (import.meta.env.MODE === "test") recorded.push(violation);
}

/** Drains violations recorded since the last take. */
export function takeFilesPresentationLivenessViolations(): FilesPresentationLivenessViolation[] {
  const out = [...recorded];
  recorded.length = 0;
  return out;
}
