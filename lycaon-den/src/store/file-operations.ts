import type { SourceOperationStatus } from "../api/types.ts";

const phases: Record<string, string> = {
  preparing: "Checking files…",
  checking: "Checking file contents…",
  preserving: "Preserving undo data…",
  copying: "Copying…",
  restoring: "Restoring…",
  verifying: "Verifying files…",
  applying: "Applying changes…",
  recording: "Recording file history…",
};

const labels: Record<string, string> = {
  createProjectSourceEntry: "Create",
  renameProjectSource: "Move",
  copyProjectSource: "Copy",
  deleteProjectSource: "Move to trash",
  undoProjectSourceHistory: "Undo",
  redoProjectSourceHistory: "Redo",
};

export function fileOperationLabel(state: SourceOperationStatus): string {
  return labels[state.operation] ?? "File operation";
}

export function fileOperationDetail(state: SourceOperationStatus): string {
  switch (state.state) {
    case "queued": return "Waiting to start…";
    case "completed": return "Completed";
    case "canceled": return "Canceled";
    case "interrupted": return "Interrupted. Retry when ready.";
    case "failed": {
      if (state.error?.message) return state.error.message;
      return "Could not complete this operation.";
    }
  }
  const phase = phases[state.phase] ?? "Working…";
  const progress: string[] = [];
  if (state.entries_processed > 0) progress.push(`${state.entries_processed.toLocaleString()} entries`);
  if (state.bytes_processed > 0) {
    const units = ["B", "KiB", "MiB", "GiB", "TiB"];
    const index = Math.min(units.length - 1, Math.floor(Math.log(state.bytes_processed) / Math.log(1024)));
    progress.push(`${(state.bytes_processed / 1024 ** index).toLocaleString(undefined, { maximumFractionDigits: 1 })} ${units[index]}`);
  }
  return progress.length ? `${phase} ${progress.join(" · ")} processed` : phase;
}

export function fileOperationRetryable(state: SourceOperationStatus): boolean {
  if (state.state === "interrupted" || state.state === "canceled") return true;
  if (state.state !== "failed") return false;
  return state.error?.retryable === true;
}
