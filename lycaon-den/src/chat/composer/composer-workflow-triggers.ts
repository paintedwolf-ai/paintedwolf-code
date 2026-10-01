import type { WorkflowSummary } from "../../api/types.ts";

export type WorkflowSlashSuggestion = {
  trigger: string;
  label: string;
  description?: string;
  workflow_id: string;
  preset_id?: string;
};

/** Structured slash rows for composer autocomplete (manifest + preset aliases). */
export function workflowSlashSuggestionsFromCatalog(
  catalog: readonly WorkflowSummary[],
): WorkflowSlashSuggestion[] {
  const out: WorkflowSlashSuggestion[] = [];
  for (const wf of catalog) {
    const main = wf.trigger?.trim();
    if (main) {
      out.push({
        trigger: main,
        label: wf.name ?? wf.id,
        description: wf.description,
        workflow_id: wf.id,
      });
    }
    for (const preset of wf.presets ?? []) {
      const alias = preset.trigger?.trim();
      if (!alias) continue;
      out.push({
        trigger: alias,
        label: preset.name ?? preset.id,
        description: preset.description ?? wf.description,
        workflow_id: wf.id,
        preset_id: preset.id,
      });
    }
  }
  return out.sort((a, b) => a.trigger.localeCompare(b.trigger));
}

/** Prefix-filtered suggestions when the composer input starts with `/`. */
export function filterWorkflowSlashSuggestions(
  text: string,
  catalog: readonly WorkflowSummary[],
): WorkflowSlashSuggestion[] {
  const prefix = text.trim().toLowerCase();
  if (!prefix.startsWith("/")) return [];
  return workflowSlashSuggestionsFromCatalog(catalog).filter((row) =>
    row.trigger.toLowerCase().startsWith(prefix),
  );
}
