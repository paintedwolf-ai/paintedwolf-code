// @vitest-environment jsdom
import { createRoot, createSignal } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";

const capture = vi.hoisted(() => ({
  enabled: true,
  record: vi.fn<(event: string, detail: Record<string, unknown>) => void>(),
}));

vi.mock("../chat/stream/den-main-thread-perf.ts", () => ({
  isPerfCaptureEnabled: () => capture.enabled,
  recordPerfDiagnostic: capture.record,
}));

import { traceWorkspaceOpening } from "./workspace-open-diagnostic.ts";

describe("workspace opening trace", () => {
  beforeEach(() => {
    capture.enabled = true;
    capture.record.mockClear();
  });

  it("records each change in what the veil waits on, then its reveal once", () => {
    const [pending, setPending] = createSignal<readonly string[]>(["session-list", "project/files-stage/tree"]);
    const [revealed, setRevealed] = createSignal(false);
    const dispose = createRoot((dispose) => {
      traceWorkspaceOpening("project:p1", pending, revealed);
      return dispose;
    });
    setPending(["session-list", "project/files-stage/tree"]);
    setPending(["paint"]);
    setRevealed(true);
    setPending(["chat-hydration"]);
    setRevealed(false);
    setRevealed(true);

    expect(capture.record.mock.calls.map(([event, detail]) => [event, detail.pending])).toEqual([
      ["workspace-opening-waiting", "session-list,project/files-stage/tree"],
      ["workspace-opening-waiting", "paint"],
      ["workspace-opening-revealed", undefined],
    ]);
    expect(capture.record.mock.calls[0]?.[1]).toMatchObject({
      key: "project:p1",
      visibility: document.visibilityState,
      focused: document.hasFocus(),
    });
    dispose();
  });

  it("records nothing while capture is off", () => createRoot((dispose) => {
    capture.enabled = false;
    traceWorkspaceOpening("project:p1", () => ["paint"], () => false);
    expect(capture.record).not.toHaveBeenCalled();
    dispose();
  }));
});
