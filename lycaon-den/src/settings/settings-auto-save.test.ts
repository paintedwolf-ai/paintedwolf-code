import { createRoot } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { createSettingsAutoSave } from "./settings-auto-save.ts";

describe("createSettingsAutoSave", () => {
  it("debounces dirty saves", async () => {
    vi.useFakeTimers();
    const save = vi.fn().mockResolvedValue(undefined);

    createRoot((dispose) => {
      const autoSave = createSettingsAutoSave({
        delayMs: 200,
        isReady: () => true,
        isDirty: () => true,
        save,
      });

      autoSave.schedule();
      autoSave.schedule();
      expect(save).not.toHaveBeenCalled();

      vi.advanceTimersByTime(200);
      expect(save).toHaveBeenCalledTimes(1);
      dispose();
    });

    vi.useRealTimers();
  });

  it("runSynced suppresses trailing saves during store hydration", async () => {
    vi.useFakeTimers();
    const save = vi.fn().mockResolvedValue(undefined);

    createRoot((dispose) => {
      const autoSave = createSettingsAutoSave({
        delayMs: 200,
        isReady: () => true,
        isDirty: () => true,
        save,
      });

      autoSave.runSynced(() => {
        autoSave.schedule();
      });
      vi.advanceTimersByTime(200);
      expect(save).not.toHaveBeenCalled();
      dispose();
    });

    vi.useRealTimers();
  });
});
