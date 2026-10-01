import { describe, expect, it, vi, afterEach } from "vitest";
import {
  ThinkingLabelHoldEngine,
} from "./thinking-label-hold.ts";

describe("ThinkingLabelHoldEngine", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows the current label immediately when thinking becomes visible", () => {
    const engine = new ThinkingLabelHoldEngine(
      3000,
      () => true,
      () => "Running tests",
      () => undefined,
    );
    engine.handleVisibilityChange(true);
    expect(engine.read()).toBe("Running tests");
    engine.dispose();
  });

  it("keeps the displayed label for holdMs then refreshes", () => {
    vi.useFakeTimers();
    let label = "Reading files";
    const engine = new ThinkingLabelHoldEngine(
      3000,
      () => true,
      () => label,
      () => undefined,
    );
    engine.handleVisibilityChange(true);
    expect(engine.read()).toBe("Reading files");
    label = "Running tests";
    expect(engine.read()).toBe("Reading files");
    vi.advanceTimersByTime(3000);
    expect(engine.read()).toBe("Running tests");
    engine.dispose();
  });

  it("continues refreshing on each hold interval while visible", () => {
    vi.useFakeTimers();
    let label = "A";
    const engine = new ThinkingLabelHoldEngine(
      3000,
      () => true,
      () => label,
      () => undefined,
    );
    engine.handleVisibilityChange(true);
    label = "B";
    vi.advanceTimersByTime(3000);
    expect(engine.read()).toBe("B");
    label = "C";
    vi.advanceTimersByTime(3000);
    expect(engine.read()).toBe("C");
    engine.dispose();
  });

  it("does not reset the hold when visibility stays true", () => {
    vi.useFakeTimers();
    let label = "Reading files";
    const engine = new ThinkingLabelHoldEngine(
      3000,
      () => true,
      () => label,
      () => undefined,
    );
    engine.handleVisibilityChange(true);
    label = "Running tests";
    engine.handleVisibilityChange(true);
    expect(engine.read()).toBe("Reading files");
    vi.advanceTimersByTime(3000);
    expect(engine.read()).toBe("Running tests");
    engine.dispose();
  });

  it("clears immediately when thinking is no longer needed", () => {
    let visible = true;
    const engine = new ThinkingLabelHoldEngine(
      3000,
      () => visible,
      () => "Waiting for workers",
      () => undefined,
    );
    engine.handleVisibilityChange(true);
    expect(engine.read()).toBe("Waiting for workers");
    visible = false;
    engine.handleVisibilityChange(false);
    expect(engine.read()).toBeUndefined();
    engine.dispose();
  });

  it("does not wait for holdMs when visibility ends early", () => {
    vi.useFakeTimers();
    let visible = true;
    let label = "Drafting report";
    const engine = new ThinkingLabelHoldEngine(
      3000,
      () => visible,
      () => label,
      () => undefined,
    );
    engine.handleVisibilityChange(true);
    label = "Thinking";
    vi.advanceTimersByTime(1500);
    visible = false;
    engine.handleVisibilityChange(false);
    expect(engine.read()).toBeUndefined();
    engine.dispose();
  });
});
