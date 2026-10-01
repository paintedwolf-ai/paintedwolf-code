// @vitest-environment jsdom
import "../../test/document-outbox-fixture.ts";
import { stubFilesClient } from "../../test/source-client-fixture.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@solidjs/testing-library";
import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import { BackendTransportError } from "../../platform/connection/request-connectivity.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createPreparation } from "../../ui/presentation.ts";
import { PresentationProvider } from "../../ui/presentation-context.tsx";
let preparation = createPreparation();
import { ProjectFilesView } from "./ProjectFilesView.tsx";
import {
  PROJECT,
  ROOTS,
  resetProjectFilesViewTest,
  sourceWorkspace,
} from "./project-files-view-test-harness.ts";
import {
  WORKSPACE_FAULT_GENERIC,
  WORKSPACE_FAULT_IDENTITY_UNSTABLE,
  WORKSPACE_IDENTITY_RECONCILE_LIMIT,
  WORKSPACE_SYNC_RETRYING,
} from "../source/files-workspace-fault.ts";
import type { ProjectRoot } from "../../api/types.ts";

function renderFilesView(client: LycaonClient, roots: ProjectRoot[] = []) {
  const appStore = createAppStore();
  appStore.actions.setSidecarStatus("connected");
  preparation = createPreparation();
  render(() => (
    <PresentationProvider preparation={preparation}>
    <ProjectFilesView
      projectId={PROJECT}
      projectName="Nomos"
      appStore={appStore}
      roots={roots}
      client={client}
    />
    </PresentationProvider>
  ));
}

describe("ProjectFilesView workspace lookup faults", () => {
  beforeEach(resetProjectFilesViewTest);
  afterEach(() => vi.useRealTimers());

  it("faults instead of spinning when listings keep naming another workspace", async () => {
    const base = getLycaonClient()!;
    // Root listings answer for workspace-a; the identity lookup never agrees.
    const getSourceWorkspace = vi.fn(async () => sourceWorkspace("workspace-b"));
    const browseProjectSource = vi.fn(
      async (_projectId: string, input: { rootId: string; dir: string }) => ({
        workspace_id: "workspace-a",
        root_id: input.rootId,
        dir: input.dir,
        watch_complete: true,
        entries: [],
      }),
    );
    renderFilesView(
      stubFilesClient({ ...base, getSourceWorkspace, browseProjectSource }),
      ROOTS,
    );

    const hold = screen.getByTestId("project-loading-stage");
    await waitFor(() => {
      expect(hold.textContent).toContain(WORKSPACE_FAULT_IDENTITY_UNSTABLE);
    });
    expect(hold.querySelector('[role="alert"]')).not.toBeNull();
    expect(getSourceWorkspace).toHaveBeenCalledTimes(
      1 + WORKSPACE_IDENTITY_RECONCILE_LIMIT,
    );
    expect(screen.getByTestId("project-files-stage-boundary").getAttribute("data-ready")).toBe(
      "false",
    );
    await waitFor(() => expect(!preparation.ready()).toBe(false));

    vi.useFakeTimers();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(getSourceWorkspace).toHaveBeenCalledTimes(
      1 + WORKSPACE_IDENTITY_RECONCILE_LIMIT,
    );
  });

  it("reports a refused lookup on the hold and lets the shell go", async () => {
    const base = getLycaonClient()!;
    const getSourceWorkspace = vi.fn(async () => {
      throw new LycaonApiError(
        "This project is not attached any more.",
        404,
        "project_not_found",
      );
    });
    renderFilesView(stubFilesClient({ ...base, getSourceWorkspace }));

    const hold = screen.getByTestId("project-loading-stage");
    await waitFor(() => {
      expect(hold.textContent).toContain("This project is not attached any more.");
    });
    expect(hold.querySelector('[role="alert"]')).not.toBeNull();
    expect(screen.getByTestId("project-files-stage-boundary").getAttribute("data-ready")).toBe(
      "false",
    );
    // The shell veil is released so the person can navigate away.
    await waitFor(() => expect(!preparation.ready()).toBe(false));

    vi.useFakeTimers();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(getSourceWorkspace).toHaveBeenCalledTimes(1);
  });

  it("names a client-side refusal without exposing its internals", async () => {
    const base = getLycaonClient()!;
    const getSourceWorkspace = vi.fn(async () => {
      throw new Error("Unknown API operation GET /v1/projects/p1/source/workspace");
    });
    renderFilesView(stubFilesClient({ ...base, getSourceWorkspace }));

    const hold = screen.getByTestId("project-loading-stage");
    await waitFor(() => {
      expect(hold.textContent).toContain(WORKSPACE_FAULT_GENERIC);
    });
    expect(hold.textContent).not.toContain("Unknown API operation");
    await waitFor(() => expect(!preparation.ready()).toBe(false));
  });

  it.each([
    ["a missing HTTP response", () => new BackendTransportError(new TypeError("Failed to fetch"), "reachable")],
    ["an active root transition", () => new LycaonApiError("Project is changing", 409, "project_mutation_in_progress")],
  ])("recovers from %s and reports the wait on the veil", async (_label, failure) => {
    const base = getLycaonClient()!;
    let calls = 0;
    const getSourceWorkspace = vi.fn(async () => {
      calls += 1;
      if (calls < 3) {
        throw failure();
      }
      return sourceWorkspace("workspace-after-retry", []);
    });
    renderFilesView(stubFilesClient({ ...base, getSourceWorkspace }));

    const hold = screen.getByTestId("project-loading-stage");
    await waitFor(() => {
      expect(hold.textContent).toContain(WORKSPACE_SYNC_RETRYING);
    });
    expect(hold.querySelector('[role="alert"]')).toBeNull();
    // The hold stays and carries its account to the shell veil.
    expect(!preparation.ready()).toBe(true);
    expect(preparation.notice()).toEqual({
      message: WORKSPACE_SYNC_RETRYING,
      tone: "waiting",
    });

    const boundary = screen.getByTestId("project-files-stage-boundary");
    await waitFor(
      () => {
        expect(boundary.getAttribute("data-ready")).toBe("true");
      },
      { timeout: 8_000 },
    );
    expect(calls).toBe(3);
    expect(!preparation.ready()).toBe(false);
    expect(preparation.notice()).toBeNull();
  }, 10_000);
});
