import { createMemo, createSignal } from "solid-js";
import type { SettingsLimitsPatch, SettingsLimitsResponse } from "../../api/types.ts";

type LimitsEdits = { [K in keyof SettingsLimitsPatch]?: Exclude<SettingsLimitsPatch[K], null> };

/** Only explicit field edits belong in a PATCH; effective inherited values stay at the host. */
export function changedLimitsFields(limits: SettingsLimitsResponse, edits: SettingsLimitsPatch): SettingsLimitsPatch {
  return Object.fromEntries(Object.entries(edits).filter(([key, value]) =>
    value !== undefined && (value === null || value !== limits[key as keyof SettingsLimitsPatch])));
}

export function createLimitsDraft(limits: () => SettingsLimitsResponse | undefined) {
  const [edits, setEdits] = createSignal<LimitsEdits>({});
  let saving = false;
  const value = createMemo(() => {
    const current = limits();
    return current ? { ...current, ...edits() } : undefined;
  });
  return {
    value,
    dirty: () => Object.keys(edits()).length > 0,
    edit<K extends keyof LimitsEdits>(key: K, next: LimitsEdits[K]) {
      setEdits((prior) => {
        const updated = { ...prior, [key]: next };
        // A revert during a save must follow the in-flight write back to the host.
        if (!saving && next === limits()?.[key]) delete updated[key];
        return updated;
      });
    },
    beginSave() { saving = true; return edits(); },
    acknowledge(sent: LimitsEdits) {
      setEdits((prior) => {
        const remaining = { ...prior };
        for (const key of Object.keys(sent) as (keyof LimitsEdits)[]) {
          if (remaining[key] === sent[key]) delete remaining[key];
        }
        return remaining;
      });
    },
    finishSave() { saving = false; },
  };
}
