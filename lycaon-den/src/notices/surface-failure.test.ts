import { createRoot, createSignal } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import { LycaonApiError } from "../api/http.ts";
import { setEngineStateForTest } from "../platform/connection/engine-supervision.ts";
import { BackendTransportError } from "../platform/connection/request-connectivity.ts";
import { createNoticeStore, registerNoticePublisher } from "./notice-store.ts";
import { selectProjectNoticeGroups } from "./notice-select.ts";
import { observeSurfaceFailure, reportSurfaceFailure } from "./surface-failure.ts";

const COPY = { code: "test_surface_unavailable", title: "Surface unavailable", suggestedAction: "Reopen it." };

function setup() {
  const store = createNoticeStore();
  registerNoticePublisher(store);
  const rows = () => selectProjectNoticeGroups(store.index()).find(group => group.projectId === "p1")?.notices ?? [];
  return { rows };
}

afterEach(() => {
  registerNoticePublisher(null);
  setEngineStateForTest({ state: "idle" });
});

describe("surface failures", () => {
  it("publishes a plain message under the surface's copy", () => {
    const { rows } = setup();
    reportSurfaceFailure(COPY, "The history could not be read.", "p1");
    expect(rows()).toMatchObject([{ code: COPY.code, title: COPY.title, message: "The history could not be read.", suggestedAction: COPY.suggestedAction }]);
  });

  it("merges repeats into one row", () => {
    const { rows } = setup();
    reportSurfaceFailure(COPY, "one", "p1");
    reportSurfaceFailure(COPY, new Error("two"), "p1");
    expect(rows()).toHaveLength(1);
    expect(rows()[0]?.repeats).toBe(2);
  });

  it("keeps the host's own copy for a typed error", () => {
    const { rows } = setup();
    reportSurfaceFailure(COPY, new LycaonApiError("The host is temporarily limiting this activity.", 429, "invalid_request" as never, { title: "That request was not valid" }), "p1");
    expect(rows()[0]).toMatchObject({ code: "invalid_request", title: "That request was not valid" });
  });

  it("leaves an unreachable engine to the app instead of each surface", () => {
    const { rows } = setup();
    reportSurfaceFailure(COPY, new BackendTransportError(new TypeError("Load failed"), "unreachable"), "p1");
    expect(rows()).toEqual([]);
    reportSurfaceFailure(COPY, new BackendTransportError(new TypeError("Load failed"), "reachable"), "p1");
    expect(rows()).toHaveLength(1);
  });

  it("reports nothing while the shell is restarting or has stopped its engine", () => {
    const { rows } = setup();
    const exit = { signal: 9, description: "killed by signal 9 (SIGKILL)" };
    for (const state of [{ state: "restarting", exit, attempt: 1 }, { state: "stopped", exit }] as const) {
      setEngineStateForTest(state);
      reportSurfaceFailure(COPY, "The history could not be read.", "p1");
      reportSurfaceFailure(COPY, new Error("Connection refused"), "p1");
    }
    expect(rows()).toEqual([]);
  });

  it("uses the surface's wording when it supplies one, even for a typed error", () => {
    const { rows } = setup();
    const changed = new LycaonApiError("History changed.", 409, "source_history_changed" as never);
    reportSurfaceFailure(COPY, changed, "p1", "Files changed on disk. Review the current tree before trying again.");
    expect(rows()[0]).toMatchObject({ code: COPY.code, message: "Files changed on disk. Review the current tree before trying again." });
    reportSurfaceFailure(COPY, new BackendTransportError(new TypeError("Load failed"), "unreachable"), "p1", "Could not update file history.");
    expect(rows()).toHaveLength(1);
  });

  it("ignores aborts and empty messages", () => {
    const { rows } = setup();
    reportSurfaceFailure(COPY, new DOMException("gone", "AbortError"), "p1");
    reportSurfaceFailure(COPY, "  ", "p1");
    reportSurfaceFailure(COPY, undefined, "p1");
    expect(rows()).toEqual([]);
  });

  it("reports each new failure a component holds and nothing when it clears", async () => {
    const { rows } = setup();
    await createRoot(async dispose => {
      try {
        const [failure, setFailure] = createSignal<string | null>(null);
        observeSurfaceFailure(COPY, failure, () => "p1");
        expect(rows()).toEqual([]);
        setFailure("first");
        await Promise.resolve();
        expect(rows()).toHaveLength(1);
        setFailure(null);
        await Promise.resolve();
        expect(rows()[0]?.repeats).toBeUndefined();
      } finally {
        dispose();
      }
    });
  });
});
