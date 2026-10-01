import { createSignal, onCleanup } from "solid-js";
import { createCoalescedAsyncScheduler } from "../store/coalesced-async.ts";

type Options = {
  delayMs?: number;
  isReady: () => boolean;
  isDirty: () => boolean;
  save: () => Promise<void>;
};

/** Debounced auto-save for settings editors (no explicit Save button). */
export function createSettingsAutoSave(options: Options) {
  const [saving, setSaving] = createSignal(false);
  let syncDepth = 0;

  const scheduler = createCoalescedAsyncScheduler(async () => {
    if (syncDepth > 0 || !options.isReady() || !options.isDirty() || saving()) {
      return;
    }
    setSaving(true);
    try {
      await options.save();
    } finally {
      setSaving(false);
    }
  }, options.delayMs ?? 400);

  onCleanup(() => scheduler.cancel());

  return {
    saving,
    schedule: () => {
      if (syncDepth > 0 || !options.isReady() || !options.isDirty()) return;
      scheduler.schedule();
    },
    cancel: () => scheduler.cancel(),
    /** Apply store/local sync without scheduling a trailing save. */
    runSynced: (fn: () => void) => {
      syncDepth++;
      scheduler.cancel();
      try {
        fn();
      } finally {
        syncDepth--;
      }
    },
  };
}
