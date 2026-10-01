import { batch, createRoot, createSignal } from "solid-js";
import { describe, expect, it } from "vitest";
import { retainStageSurface, useResidentStack } from "../ui/resident-surfaces.ts";
import type { StageColumnResolution } from "./stage-placement.ts";
import { useStageLayoutPresentation } from "./stage-layout-presentation.ts";

const SPLIT = { splitLive: true, stageId: "files" } as const;
const FULL = { splitLive: false, stageId: null } as const;

function harness() {
  const [projectId, setProjectId] = createSignal<string | null>("p1");
  const [mounted, setMounted] = createSignal(true);
  const [column, setColumn] = createSignal<StageColumnResolution>(SPLIT);
  const [requested, setRequested] = createSignal<string | null>("stage:p1:files");
  let stack!: ReturnType<typeof useResidentStack>;
  let layout!: () => StageColumnResolution;
  const dispose = createRoot((dispose) => {
    stack = useResidentStack(requested, { retain: (key) => retainStageSurface(key, projectId()) });
    layout = useStageLayoutPresentation({
      projectId, mounted, column, requestedSurface: requested, displayedSurface: () => stack.displayed(),
    });
    return dispose;
  });
  stack.markReady("stage:p1:files");
  const navigate = (key: string | null, next: StageColumnResolution = FULL) => batch(() => {
    setColumn(next);
    setRequested(key);
  });
  return { stack, layout, navigate, setProjectId, setMounted, dispose };
}

describe("stage layout publication", () => {
  it("retains split geometry until the resident Settings surface publishes", () => {
    const h = harness();
    try {
      h.navigate("settings");
      expect(h.stack.pending()).toBe("settings");
      expect(h.stack.displayed()).toBe("stage:p1:files");
      expect(h.layout()).toEqual(SPLIT);
      h.stack.markReady("settings");
      expect(h.stack.displayed()).toBe("settings");
      expect(h.layout()).toEqual(FULL);
      h.navigate("stage:p1:files", SPLIT);
      h.navigate("settings");
      expect(h.layout()).toEqual(FULL);
    } finally { h.dispose(); }
  });

  it("ignores a superseded Settings completion and holds the next destination", () => {
    const h = harness();
    try {
      h.navigate("settings");
      h.navigate("project-config:p1");
      h.stack.markReady("settings");
      expect(h.layout()).toEqual(SPLIT);
      h.stack.markReady("project-config:p1");
      expect(h.layout()).toEqual(FULL);
    } finally { h.dispose(); }
  });

  it("keeps split layout when Settings is canceled back to the conversation", () => {
    const h = harness();
    try {
      h.navigate("settings");
      h.navigate("stage:p1:files", SPLIT);
      h.stack.markReady("settings");
      expect(h.stack.displayed()).toBe("stage:p1:files");
      expect(h.layout()).toEqual(SPLIT);
    } finally { h.dispose(); }
  });

  it("applies an explicit inline layout immediately without a new surface", () => {
    const h = harness();
    try {
      h.navigate("stage:p1:files", { splitLive: false, stageId: "files" });
      expect(h.layout()).toEqual({ splitLive: false, stageId: "files" });
      h.navigate(null);
      expect(h.layout()).toEqual(FULL);
    } finally { h.dispose(); }
  });

  it.each(["project", "unmount"])("drops outgoing geometry on %s change", (change) => {
    const h = harness();
    try {
      h.navigate("settings");
      if (change === "project") h.setProjectId("p2");
      else h.setMounted(false);
      expect(h.layout()).toEqual(FULL);
    } finally { h.dispose(); }
  });
});
