// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { createRoot, createSignal } from "solid-js";
import type { LycaonClient } from "../api/client.ts";
import type { ReplaceProjectViewRequest } from "../api/types.ts";
import { composeViews, offerViewFacet, setStageShown, startViewReporter, VIEW_RENEW_MS, type ViewFacet } from "./view-report.ts";

function offerFor(projectId: string, facet: () => ViewFacet | null) {
  return { projectId: () => projectId, facet };
}

describe("composeViews", () => {
  it("merges facets per project and keeps the first surface's answer", () => {
    const views = composeViews([
      offerFor("p", () => ({ editor: { document_id: "doc" } })),
      offerFor("p", () => ({ editor: { document_id: "other" }, folder: { root_id: "r", path: "src" } })),
      offerFor("q", () => ({ findings: [] })),
      offerFor("q", () => null),
    ]);
    expect(views.get("p")).toEqual({ editor: { document_id: "doc" }, folder: { root_id: "r", path: "src" } });
    expect(views.get("q")).toEqual({});
  });
});

describe("startViewReporter", () => {
  afterEach(() => vi.useRealTimers());

  function harness() {
    vi.useFakeTimers();
    const calls: Array<[string, ReplaceProjectViewRequest]> = [];
    const client = { replaceProjectView: vi.fn(async (projectId: string, req: ReplaceProjectViewRequest) => { calls.push([projectId, req]); }) };
    const stop = startViewReporter(() => client as unknown as LycaonClient);
    return { calls, stop };
  }

  it("reports a shown surface, sends changes, renews it, and withdraws it once nothing is shown", async () => {
    const { calls, stop } = harness();
    setStageShown(true);
    const [facet, setFacet] = createSignal<ViewFacet | null>({ folder: { root_id: "r", path: "src" } });
    const dispose = createRoot((dispose) => { offerViewFacet(() => "p", facet); return dispose; });
    await vi.advanceTimersByTimeAsync(200);
    expect(calls.at(-1)?.[1].folder).toEqual({ root_id: "r", path: "src" });

    setFacet({ folder: { root_id: "r", path: "src/api" } });
    await vi.advanceTimersByTimeAsync(200);
    expect(calls.at(-1)?.[1].folder).toEqual({ root_id: "r", path: "src/api" });

    const sent = calls.length;
    await vi.advanceTimersByTimeAsync(VIEW_RENEW_MS);
    expect(calls.length).toBe(sent + 1);

    dispose();
    await vi.advanceTimersByTimeAsync(200);
    expect(calls.at(-1)?.[0]).toBe("p");
    expect(calls.at(-1)?.[1].folder).toBeNull();
    const withdrawn = calls.length;
    await vi.advanceTimersByTimeAsync(VIEW_RENEW_MS);
    expect(calls.length).toBe(withdrawn);
    stop();
  });

  it("reports nothing while the stage pane is hidden", async () => {
    const { calls, stop } = harness();
    setStageShown(false);
    const dispose = createRoot((dispose) => { offerViewFacet(() => "p", () => ({ folder: { root_id: "r", path: "src" } })); return dispose; });
    await vi.advanceTimersByTimeAsync(VIEW_RENEW_MS + 200);
    expect(calls).toHaveLength(0);
    dispose();
    stop();
  });
});
