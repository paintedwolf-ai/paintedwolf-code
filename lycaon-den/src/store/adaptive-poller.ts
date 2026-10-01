type AdaptivePoller = {
  setEnabled: (enabled: boolean) => void;
  cancel: () => void;
};

/** Polls one request at a time with bounded backoff. */
export function createAdaptivePoller(
  run: () => Promise<void>,
  floorMs: number,
  ceilingMs: number,
): AdaptivePoller {
  const floor = Math.max(0, floorMs);
  const ceiling = Math.max(floor, ceilingMs);
  let delay = floor;
  let enabled = false;
  let running = false;
  let timer: ReturnType<typeof setTimeout> | undefined;

  const clear = () => {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  };

  const arm = () => {
    if (!enabled || running || timer !== undefined) return;
    const tick = async () => {
      timer = undefined;
      if (!enabled) return;
      running = true;
      try {
        await run();
      } catch {
        // Failed polls retry with the same bounded backoff.
      } finally {
        running = false;
        delay = Math.min(ceiling, Math.max(floor, delay * 2));
        arm();
      }
    };
    timer = setTimeout(() => void tick(), delay);
  };

  return {
    setEnabled(next) {
      if (enabled === next) return;
      enabled = next;
      if (!enabled) {
        clear();
        delay = floor;
        return;
      }
      arm();
    },
    cancel() {
      enabled = false;
      delay = floor;
      clear();
    },
  };
}
