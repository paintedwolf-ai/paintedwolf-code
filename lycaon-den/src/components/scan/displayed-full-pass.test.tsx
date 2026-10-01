import { createRoot, createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { CodeScan, SecurityFullPass, SecurityOverview } from "../../api/types.ts";
import { MIN_VISIBLE_MS } from "../../ui/min-visible-hold.ts";
import { createDisplayedFullPass } from "./displayed-full-pass.ts";

function pass(id: string, status: CodeScan["status"]): SecurityFullPass {
  return {
    assessment_id: id,
    requested_at: "2026-09-10T17:15:57Z",
    members: [{
      scanner_id: "sast",
      phase: "started",
      scan: { id: `${id}-scan`, categories: ["sast"], scanner_id: "sast", status, long_running: false, findings_count: 0, created_at: "2026-09-10T17:15:57Z" },
    }],
  };
}

function overview(patch: Partial<SecurityOverview>): SecurityOverview {
  return { project_id: "p1", enabled: true, scanners: [], introduced_since_baseline: 0, fixed_since_baseline: 0, ...patch };
}

function mount(initial: SecurityOverview | null) {
  const [value, setValue] = createSignal<SecurityOverview | null>(initial);
  const [project, setProject] = createSignal("p1");
  let dispose!: () => void;
  const displayed = createRoot((done) => {
    dispose = done;
    return createDisplayedFullPass(value, project);
  });
  return { displayed, setValue, setProject, dispose };
}

describe("createDisplayedFullPass", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("presents the host's running pass live", () => {
    const running = pass("a1", "running");
    const { displayed, dispose } = mount(overview({ running }));
    expect(displayed()).toEqual({ pass: running, live: true });
    dispose();
  });

  it("holds a pass that finished within the minimum interval at its final state", () => {
    vi.useFakeTimers();
    const { displayed, setValue, dispose } = mount(overview({ running: pass("a1", "running") }));
    vi.advanceTimersByTime(200);
    const finished = { ...pass("a1", "complete"), completed_at: "2026-09-10T17:15:58Z" };
    setValue(overview({ last_full: finished }));
    expect(displayed()).toEqual({ pass: finished, live: false });

    vi.advanceTimersByTime(MIN_VISIBLE_MS - 201);
    expect(displayed()?.live).toBe(false);
    vi.advanceTimersByTime(1);
    expect(displayed()).toBeNull();
    dispose();
  });

  it("lets a pass that was shown longer than the interval leave at once", () => {
    vi.useFakeTimers();
    const { displayed, setValue, dispose } = mount(overview({ running: pass("a1", "running") }));
    vi.advanceTimersByTime(MIN_VISIBLE_MS + 1);
    setValue(overview({ last_full: pass("a1", "complete") }));
    expect(displayed()).toBeNull();
    dispose();
  });

  it("never presents another project's pass", () => {
    vi.useFakeTimers();
    const { displayed, setValue, setProject, dispose } = mount(overview({ running: pass("a1", "running") }));
    setProject("p2");
    setValue(null);
    expect(displayed()).toBeNull();
    dispose();
  });

  it("shows a newer running pass at once", () => {
    vi.useFakeTimers();
    const { displayed, setValue, dispose } = mount(overview({ running: pass("a1", "running") }));
    const next = pass("a2", "pending");
    setValue(overview({ running: next }));
    expect(displayed()).toEqual({ pass: next, live: true });
    dispose();
  });
});
