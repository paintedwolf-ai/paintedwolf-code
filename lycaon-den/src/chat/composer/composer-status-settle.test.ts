import { describe, expect, it, vi } from "vitest";
import {
  COMPOSER_STATUS_HIDE_SETTLE_MS,
  SettledVisibilityEngine,
} from "./composer-status-settle.ts";

describe("SettledVisibilityEngine", () => {
  it("shows immediately and hides only after settle", () => {
    vi.useFakeTimers();
    let desired = false;
    const onChange = vi.fn();
    const engine = new SettledVisibilityEngine(
      COMPOSER_STATUS_HIDE_SETTLE_MS,
      () => desired,
      onChange,
    );
    expect(engine.read()).toBe(false);

    desired = true;
    engine.handleDesired(true);
    expect(engine.read()).toBe(true);
    expect(onChange).toHaveBeenCalledTimes(1);

    desired = false;
    engine.handleDesired(false);
    expect(engine.read()).toBe(true);
    vi.advanceTimersByTime(COMPOSER_STATUS_HIDE_SETTLE_MS - 1);
    expect(engine.read()).toBe(true);
    vi.advanceTimersByTime(1);
    expect(engine.read()).toBe(false);
    expect(onChange).toHaveBeenCalledTimes(2);

    engine.dispose();
    vi.useRealTimers();
  });

  it("cancels a pending hide when desired returns true", () => {
    vi.useFakeTimers();
    let desired = true;
    const engine = new SettledVisibilityEngine(
      COMPOSER_STATUS_HIDE_SETTLE_MS,
      () => desired,
      () => {},
    );
    engine.handleDesired(true);
    desired = false;
    engine.handleDesired(false);
    desired = true;
    engine.handleDesired(true);
    vi.advanceTimersByTime(COMPOSER_STATUS_HIDE_SETTLE_MS + 50);
    expect(engine.read()).toBe(true);
    engine.dispose();
    vi.useRealTimers();
  });
});
