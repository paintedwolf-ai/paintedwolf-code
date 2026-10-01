import type { WorkflowSummary } from "../../api/types.ts";
import { escapeRegExp } from "../../utils/escape-regexp.ts";
import type { CatalogPickerRow } from "../../workflow/workflows-drawer-model.ts";
import {
  workflowSlashSuggestionsFromCatalog,
  type WorkflowSlashSuggestion,
} from "../composer/composer-workflow-triggers.ts";

/** Session-local armed catalog workflow — start happens on the next composer send. */
export type ArmedWorkflow = {
  workflow_id: string;
  workflow_version: string;
  preset_id?: string;
  /** Catalog slash trigger used to synthesize the start line (e.g. `/plan`). */
  trigger: string;
  label: string;
  request?: WorkflowSummary["request"];
};

export type ComposerWorkflowStartResolution =
  | { kind: "pass"; text: string }
  | { kind: "arm"; armed: ArmedWorkflow }
  | { kind: "synthesize"; text: string };


function suggestionToArmed(
  row: WorkflowSlashSuggestion,
  catalog: readonly WorkflowSummary[],
): ArmedWorkflow | null {
  const wf = catalog.find((entry) => entry.id === row.workflow_id);
  if (!wf) return null;
  return {
    workflow_id: wf.id,
    workflow_version: wf.version,
    preset_id: row.preset_id,
    trigger: row.trigger,
    label: row.label,
    request: wf.request,
  };
}

/** Longest catalog trigger match at the start of `text` (case-insensitive). */
export function matchWorkflowSlashPrefix(
  text: string,
  catalog: readonly WorkflowSummary[],
): { suggestion: WorkflowSlashSuggestion; remainder: string } | null {
  const trimmed = text.trim();
  if (!trimmed.startsWith("/")) return null;
  const rows = workflowSlashSuggestionsFromCatalog(catalog);
  let best: WorkflowSlashSuggestion | null = null;
  for (const row of rows) {
    const re = new RegExp(`^${escapeRegExp(row.trigger)}(?:\\s+|$)`, "i");
    if (!re.test(trimmed)) continue;
    if (!best || row.trigger.length > best.trigger.length) best = row;
  }
  if (!best) return null;
  const remainder = trimmed.slice(best.trigger.length).trim();
  return { suggestion: best, remainder };
}

export function armedWorkflowFromPickerRow(wf: CatalogPickerRow): ArmedWorkflow | null {
  const trigger =
    (wf.start_preset_id
      ? wf.presets?.find((p) => p.id === wf.start_preset_id)?.trigger
      : undefined
    )?.trim() ||
    wf.trigger?.trim() ||
    "";
  if (!trigger) return null;
  const label =
    (wf.start_preset_id
      ? wf.presets?.find((p) => p.id === wf.start_preset_id)?.name
      : undefined) ||
    wf.name ||
    wf.id;
  return {
    workflow_id: wf.id,
    workflow_version: wf.version,
    preset_id: wf.start_preset_id,
    trigger,
    label,
    request: wf.request,
  };
}

export function synthesizeArmedSlashLine(armed: ArmedWorkflow, ask: string): string {
  const remainder = ask.trim();
  return remainder ? `${armed.trigger} ${remainder}` : armed.trigger;
}

/** Grammatical for any catalog name, verb ("Plan") or noun ("Security") alike. */
export function armedComposerPlaceholder(armed: ArmedWorkflow): string {
  return armed.request?.question?.trim() || `What should ${armed.label} focus on?`;
}

/** Bare workflow slashes arm locally; explicit slashes take precedence over the armed workflow. */
export function resolveComposerWorkflowStart(args: {
  text: string;
  catalog: readonly WorkflowSummary[];
  armed: ArmedWorkflow | null;
}): ComposerWorkflowStartResolution {
  const trimmed = args.text.trim();
  const slash = matchWorkflowSlashPrefix(trimmed, args.catalog);
  if (slash) {
    if (!slash.remainder) {
      const armed = suggestionToArmed(slash.suggestion, args.catalog);
      if (armed) return { kind: "arm", armed };
      return { kind: "pass", text: trimmed };
    }
    return { kind: "pass", text: trimmed };
  }
  if (args.armed) {
    return {
      kind: "synthesize",
      text: synthesizeArmedSlashLine(args.armed, trimmed),
    };
  }
  return { kind: "pass", text: trimmed };
}
