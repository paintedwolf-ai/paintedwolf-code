import { render } from "@solidjs/testing-library";
import { createSignal, getOwner } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { refreshSessionWorkers } from "../../chat/actions/board-actions.ts";
import { flushWorkerTranscriptCoalesce } from "../../chat/worker/worker-transcript-coalesce.ts";
import { SessionWorkersDrawer } from "./SessionWorkersDrawer.tsx";

vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: vi.fn() }));
vi.mock("../../chat/actions/board-actions.ts", () => ({ refreshSessionWorkers: vi.fn(async () => {}) }));
vi.mock("../../chat/worker/worker-transcript-coalesce.ts", () => ({ flushWorkerTranscriptCoalesce: vi.fn() }));
vi.mock("./WorkersDrawer.tsx", () => ({ WorkersDrawer: () => null }));

const worker = {
  id: "job", parent_session_id: "session", child_session_id: "child",
  agent_type: "implementer", status: "running" as const, created_at: "2026-01-01T00:00:00Z",
};

function mountDrawer(initialOpen: boolean) {
  const appStore = createAppStore();
  appStore.actions.setWorkers([worker]);
  const [open, setOpen] = createSignal(initialOpen);
  const unownedReads: boolean[] = [];
  const props = {
    appStore, projects: [], projectDir: "/fixture", sessionId: "session",
    selectedId: worker.id, backgroundHydrate: true, onClose: () => {},
    get open() {
      // JSX logical expressions may allocate a memo in their generated getter.
      if (!getOwner()) unownedReads.push(true);
      return open();
    },
  };
  const view = render(() => <SessionWorkersDrawer {...props} />);
  return { ...view, appStore, setOpen, unownedReads };
}

beforeEach(() => {
  vi.mocked(refreshSessionWorkers).mockResolvedValue(undefined);
});

afterEach(() => {
  vi.useRealTimers();
  vi.clearAllMocks();
});

describe("worker drawer hydration lifetime", () => {
  it("flushes pending rows and waits for the roster before hydrating an opened drawer", async () => {
    vi.useFakeTimers();
    let refreshed!: () => void;
    vi.mocked(refreshSessionWorkers).mockImplementation(() => new Promise<void>((resolve) => { refreshed = resolve; }));
    const listSessionMessages = vi.fn(async () => ({ messages: [], watermark: 0 }));
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ listSessionMessages }));
    const view = mountDrawer(false);
    vi.mocked(flushWorkerTranscriptCoalesce).mockClear();
    view.setOpen(true);
    await vi.advanceTimersByTimeAsync(250);
    expect(flushWorkerTranscriptCoalesce).toHaveBeenCalled();
    expect(refreshSessionWorkers).toHaveBeenCalledOnce();
    expect(vi.mocked(flushWorkerTranscriptCoalesce).mock.invocationCallOrder[0])
      .toBeLessThan(vi.mocked(refreshSessionWorkers).mock.invocationCallOrder[0]!);
    expect(listSessionMessages).not.toHaveBeenCalled();
    refreshed();
    await vi.advanceTimersByTimeAsync(0);
    expect(listSessionMessages).toHaveBeenCalledOnce();
    view.unmount();
  });

  it("retains reactive prop lifetimes across background hydration, opening and retries", async () => {
    vi.useFakeTimers();
    const listSessionMessages = vi.fn(async () => ({ messages: [], watermark: 0 }));
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ listSessionMessages }));
    const view = mountDrawer(false);
    await vi.advanceTimersByTimeAsync(250);
    expect(listSessionMessages).toHaveBeenCalled();
    view.setOpen(true);
    await vi.advanceTimersByTimeAsync(4000);
    expect(listSessionMessages.mock.calls.length).toBeGreaterThan(2);
    view.setOpen(false);
    await vi.advanceTimersByTimeAsync(250);
    expect(view.unownedReads).toEqual([]);
    view.unmount();
  });

  it.each([false, true])("retires pending hydration when removed (open=%s)", async (open) => {
    vi.useFakeTimers();
    let release!: (value: { messages: []; watermark: number }) => void;
    const listSessionMessages = vi.fn(() => new Promise<{ messages: []; watermark: number }>((resolve) => {
      release = resolve;
    }));
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ listSessionMessages }));
    const view = mountDrawer(open);
    const apply = vi.spyOn(view.appStore.actions, "applyWorkerTranscriptRows");
    await vi.advanceTimersByTimeAsync(250);
    expect(listSessionMessages).toHaveBeenCalledOnce();
    view.unmount();
    release({ messages: [], watermark: 0 });
    await vi.advanceTimersByTimeAsync(5000);
    expect(listSessionMessages).toHaveBeenCalledOnce();
    expect(apply).not.toHaveBeenCalled();
    expect(view.unownedReads).toEqual([]);
  });
});
