// @vitest-environment jsdom
import { createRoot, createSignal } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ResidentStack } from "../../ui/resident-surfaces.ts";

const capture = vi.hoisted(() => ({
  enabled: true,
  record: vi.fn<(event: string, detail: Record<string, unknown>) => void>(),
}));

vi.mock("../../chat/stream/den-main-thread-perf.ts", () => ({
  isPerfCaptureEnabled: () => capture.enabled,
  recordPerfDiagnostic: capture.record,
}));

import { shellPresentsNothing, traceShellPresentation, type ShellPresentationFacts } from "./shell-presentation-trace.ts";

function stack(displayed: () => string | null): ResidentStack {
  return {
    keys: () => [],
    presence: () => "idle",
    pending: () => null,
    displayed,
    generation: () => 0,
    markReady: () => undefined,
    withdraw: () => undefined,
  };
}

function shell() {
  const [veiled, setVeiled] = createSignal(false);
  const [stageShown, setStageShown] = createSignal<string | null>("home");
  const [chatShown, setChatShown] = createSignal<string | null>(null);
  const [conversationHidden, setConversationHidden] = createSignal<"preview" | "hidden" | null>(null);
  const [contextHidden, setContextHidden] = createSignal(false);
  const facts: ShellPresentationFacts = {
    veiled,
    stageStack: stack(stageShown),
    chatStack: stack(chatShown),
    showStageColumn: () => true,
    contextHidden,
    conversationHidden,
    nav: () => "projects",
    stageScope: () => ({ phase: "ready", project_id: "p1", session_id: "s1" }),
    presentedSplitLive: () => true,
    navUserCollapsed: () => true,
    navAutomaticallyCollapsed: () => false,
  };
  return { facts, setVeiled, setStageShown, setChatShown, setConversationHidden, setContextHidden };
}

describe("shell presentation trace", () => {
  beforeEach(() => {
    capture.enabled = true;
    capture.record.mockClear();
  });

  it("counts a column as shown only when its published surface is not hidden", () => createRoot((dispose) => {
    const view = shell();
    expect(shellPresentsNothing(view.facts)).toBe(false);
    view.setContextHidden(true);
    expect(shellPresentsNothing(view.facts)).toBe(true);
    view.setChatShown("chat:p1:s1");
    expect(shellPresentsNothing(view.facts)).toBe(false);
    view.setConversationHidden("hidden");
    expect(shellPresentsNothing(view.facts)).toBe(true);
    view.setVeiled(true);
    expect(shellPresentsNothing(view.facts)).toBe(false);
    dispose();
  }));

  it("records when the window empties and how long it stayed empty", () => createRoot((dispose) => {
    const view = shell();
    traceShellPresentation(view.facts);
    expect(capture.record).not.toHaveBeenCalled();
    view.setStageShown(null);
    view.setChatShown(null);
    view.setStageShown("project:p1:files");

    expect(capture.record.mock.calls.map(([event]) => event)).toEqual(["workspace-empty", "workspace-empty-ended"]);
    expect(capture.record.mock.calls[0]?.[1]).toMatchObject({
      nav: "projects",
      scope: "ready",
      project: true,
      split: true,
      nav_collapsed: "user",
      visibility: document.visibilityState,
    });
    expect(capture.record.mock.calls[1]?.[1]).toMatchObject({ stage_displayed: "project:p1:files" });
    expect(capture.record.mock.calls[1]?.[1]?.duration_ms).toEqual(expect.any(Number));
    dispose();
  }));

  it("records nothing while capture is off", () => createRoot((dispose) => {
    capture.enabled = false;
    const view = shell();
    traceShellPresentation(view.facts);
    view.setStageShown(null);
    expect(capture.record).not.toHaveBeenCalled();
    dispose();
  }));
});
