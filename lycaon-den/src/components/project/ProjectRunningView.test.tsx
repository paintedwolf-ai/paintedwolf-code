// @vitest-environment jsdom
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import type { LycaonClient } from "../../api/client.ts";
import type {
  BackgroundProcessStopResult,
  ProjectBackgroundProcess,
} from "../../api/types.ts";
import {
  registerOpenRunningStageSink,
  resetOpenRunningForTests,
} from "../../platform/navigation/open-running.ts";
import {
  applyProjectBackgroundEvent,
  applyProjectBackgroundSessionEvent,
  applyProjectBackgroundSnapshot,
  patchProjectBackgroundSession,
  resetProjectBackgroundForTests,
} from "../../project/project-background-store.ts";
import { syncContextNavPrefsFromSnapshot } from "../../settings/editor/context-nav-prefs.ts";
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import { createAppStore } from "../../store/app-state.ts";
import { ProjectContextZone } from "../nav/ProjectContextZone.tsx";
import { ProjectRunningView } from "./ProjectRunningView.tsx";

function makeProcess(
  overrides: Partial<ProjectBackgroundProcess> = {},
): ProjectBackgroundProcess {
  return {
    process_id: "bg_1",
    project_id: "proj_1",
    command: "bun run dev",
    working_dir: "apps/web",
    ports: ["127.0.0.1:5173"],
    running: true,
    started_at: "2026-04-20T12:00:00Z",
    session_id: "sess_1",
    session_title: "Web dev server",
    session_archived: false,
    ...overrides,
  };
}

describe("ProjectRunningView", () => {
  beforeEach(() => {
    resetProjectBackgroundForTests();
    resetOpenRunningForTests();
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    syncContextNavPrefsFromSnapshot();
  });

  it("renders a neutral loading or disconnected state before hydration without claiming no commands are running", () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("disconnected");

    render(() => (
      <ProjectRunningView
        projectId="proj_1"
        appStore={appStore}
        onOpenSession={() => undefined}
      />
    ));

    expect(
      screen.getByTestId("project-running-unhydrated").textContent,
    ).toContain("Reconnecting to backend…");
    expect(screen.queryByTestId("project-running-empty")).toBeNull();
  });

  it("renders the empty state when hydrated and connected with no running commands", () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    applyProjectBackgroundSnapshot("proj_1", {
      revision: 1,
      processes: [],
    });

    render(() => (
      <ProjectRunningView
        projectId="proj_1"
        appStore={appStore}
        onOpenSession={() => undefined}
      />
    ));

    expect(screen.getByTestId("project-running-empty").textContent).toBe(
      "No background commands are running. Servers and watchers an agent starts appear here until they are stopped.",
    );
  });

  it("groups running commands by chat, updates live on start/rename/archive, and opens the owning chat", () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    applyProjectBackgroundSnapshot("proj_1", {
      revision: 1,
      processes: [],
    });

    const onOpenSession = vi.fn();
    render(() => (
      <ProjectRunningView
        projectId="proj_1"
        appStore={appStore}
        onOpenSession={onOpenSession}
      />
    ));

    expect(screen.getByTestId("project-running-empty")).toBeTruthy();

    // Start a background command live without reloading.
    applyProjectBackgroundEvent({
      project_id: "proj_1",
      process_id: "bg_1",
      session_id: "sess_1",
      action: "started",
      revision: 2,
      running: true,
      ports: ["127.0.0.1:5173"],
      process: makeProcess({
        process_id: "bg_1",
        command: "vite --port 5173",
        working_dir: "packages/ui",
        ports: ["127.0.0.1:5173"],
        started_at: "2026-04-20T12:00:00Z",
        session_id: "sess_1",
        session_title: "UI work",
      }),
    });

    expect(screen.queryByTestId("project-running-empty")).toBeNull();
    expect(
      screen.getByTestId("project-running-group-title-sess_1").textContent,
    ).toBe("UI work");
    expect(screen.getByTestId("project-running-command-bg_1").textContent).toBe(
      "vite --port 5173",
    );
    expect(screen.getByTestId("project-running-ports-bg_1").textContent).toBe(
      "Port 127.0.0.1:5173",
    );
    expect(screen.getByTestId("project-running-workdir-bg_1").textContent).toBe(
      "packages/ui",
    );
    expect(
      screen
        .getByTestId("project-running-started-bg_1")
        .getAttribute("data-tip"),
    ).toBe("2026-04-20T12:00:00Z");

    // Archive the owning chat -> Archived indicator appears.
    patchProjectBackgroundSession("proj_1", "sess_1", {
      session_archived: true,
    });
    expect(
      screen.getByTestId("project-running-group-archived-sess_1").textContent,
    ).toBe("Archived");

    // Rename and unarchive the chat while Running is open.
    applyProjectBackgroundSessionEvent({
      id: "sess_1",
      project_id: "proj_1",
      action: "updated",
      status: "idle",
      title: "Design system server",
      archived: false,
    });
    expect(
      screen.getByTestId("project-running-group-title-sess_1").textContent,
    ).toBe("Design system server");
    expect(
      screen.queryByTestId("project-running-group-archived-sess_1"),
    ).toBeNull();

    // Open chat button invokes onOpenSession.
    fireEvent.click(screen.getByTestId("project-running-open-chat-bg_1"));
    expect(onOpenSession).toHaveBeenCalledWith("sess_1");
  });

  it("stops a running command with pending state until the end event arrives, then moves it to Recently ended with Stopped by you", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    applyProjectBackgroundSnapshot("proj_1", {
      revision: 1,
      processes: [
        makeProcess({ process_id: "bg_1", command: "npm run watch" }),
      ],
    });

    let resolveStop!: (res: BackgroundProcessStopResult) => void;
    const client = {
      stopProjectBackgroundProcess: vi.fn(
        () =>
          new Promise<BackgroundProcessStopResult>((resolve) => {
            resolveStop = resolve;
          }),
      ),
    } as unknown as LycaonClient;

    render(() => (
      <ProjectRunningView
        projectId="proj_1"
        appStore={appStore}
        client={client}
        onOpenSession={() => undefined}
      />
    ));

    const stopBtn = screen.getByRole("button", { name: "Stop npm run watch" });
    expect(stopBtn.textContent).toBe("Stop");
    expect((stopBtn as HTMLButtonElement).disabled).toBe(false);

    fireEvent.click(stopBtn);

    expect(client.stopProjectBackgroundProcess).toHaveBeenCalledWith(
      "proj_1",
      "bg_1",
    );
    expect(stopBtn.textContent).toBe("Stopping…");
    expect((stopBtn as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryByTestId("project-running-ended-section")).toBeNull();

    // Stop HTTP call accepts the request while process is still exiting.
    resolveStop({
      process_id: "bg_1",
      stop_requested: true,
      running: true,
    });
    await Promise.resolve();

    // Still in Running with pending state.
    expect(screen.getByTestId("project-running-row-bg_1")).toBeTruthy();
    expect(stopBtn.textContent).toBe("Stopping…");
    expect(screen.queryByTestId("project-running-ended-section")).toBeNull();

    // End event arrives from the host.
    applyProjectBackgroundEvent({
      project_id: "proj_1",
      process_id: "bg_1",
      session_id: "sess_1",
      action: "ended",
      revision: 2,
      running: false,
      termination_reason: "stopped_by_user",
      process: makeProcess({
        process_id: "bg_1",
        command: "npm run watch",
        running: false,
        ports: [],
        ended_at: "2026-04-20T12:05:00Z",
        termination_reason: "stopped_by_user",
      }),
    });

    await waitFor(() => {
      expect(screen.queryByTestId("project-running-row-bg_1")).toBeNull();
    });

    // Recently ended section is collapsed by default.
    const endedToggle = screen.getByTestId("project-running-ended-toggle");
    expect(endedToggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByTestId("project-running-ended-list")).toBeNull();

    fireEvent.click(endedToggle);
    expect(endedToggle.getAttribute("aria-expanded")).toBe("true");
    expect(
      screen.getByTestId("project-running-ended-reason-bg_1").textContent,
    ).toBe("Stopped by you");
  });

  it("clears pending state, surfaces the error, and allows retry when stopping fails", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    applyProjectBackgroundSnapshot("proj_1", {
      revision: 1,
      processes: [
        makeProcess({ process_id: "bg_1", command: "python -m http.server" }),
      ],
    });

    const stopFn = vi
      .fn<() => Promise<BackgroundProcessStopResult>>()
      .mockRejectedValueOnce(new Error("Permission denied"))
      .mockResolvedValueOnce({
        process_id: "bg_1",
        stop_requested: true,
        running: true,
      });
    const client = {
      stopProjectBackgroundProcess: stopFn,
    } as unknown as LycaonClient;

    render(() => (
      <ProjectRunningView
        projectId="proj_1"
        appStore={appStore}
        client={client}
        onOpenSession={() => undefined}
      />
    ));

    const stopBtn = screen.getByRole("button", {
      name: "Stop python -m http.server",
    });
    fireEvent.click(stopBtn);

    await waitFor(() => {
      expect(
        screen.getByTestId("project-running-stop-error-bg_1").textContent,
      ).toBe("Permission denied");
    });
    expect(stopBtn.textContent).toBe("Stop");
    expect((stopBtn as HTMLButtonElement).disabled).toBe(false);

    // Retry clears the error and enters pending-stop state.
    fireEvent.click(stopBtn);
    expect(stopFn).toHaveBeenCalledTimes(2);
    expect(screen.queryByTestId("project-running-stop-error-bg_1")).toBeNull();
    expect(stopBtn.textContent).toBe("Stopping…");
  });

  it("updates the Running nav badge as commands start and end", () => {
    render(() => (
      <ProjectContextZone
        projectId="proj_1"
        onOpenStage={() => undefined}
        isStageActive={(id) => id === "running"}
        isStageAvailable={() => true}
      />
    ));

    expect(screen.getByTestId("project-running-entry")).toBeTruthy();
    expect(screen.queryByTestId("project-context-badge-running")).toBeNull();

    applyProjectBackgroundSnapshot("proj_1", {
      revision: 1,
      processes: [
        makeProcess({ process_id: "bg_1" }),
        makeProcess({ process_id: "bg_2", command: "redis-server" }),
      ],
    });

    expect(
      screen.getByTestId("project-context-badge-running").textContent,
    ).toBe("2");

    applyProjectBackgroundEvent({
      project_id: "proj_1",
      process_id: "bg_1",
      session_id: "sess_1",
      action: "ended",
      revision: 2,
      running: false,
      termination_reason: "exited",
      process: makeProcess({
        process_id: "bg_1",
        running: false,
        termination_reason: "exited",
      }),
    });

    expect(
      screen.getByTestId("project-context-badge-running").textContent,
    ).toBe("1");

    applyProjectBackgroundEvent({
      project_id: "proj_1",
      process_id: "bg_2",
      session_id: "sess_1",
      action: "ended",
      revision: 3,
      running: false,
      termination_reason: "exited",
      process: makeProcess({
        process_id: "bg_2",
        running: false,
        termination_reason: "exited",
      }),
    });

    expect(screen.queryByTestId("project-context-badge-running")).toBeNull();
  });

  it("routes openRunningStage requests to the registered sink", async () => {
    const { openRunningStage } = await import(
      "../../platform/navigation/open-running.ts"
    );
    const sink = vi.fn();
    openRunningStage({ projectId: "proj_1" });
    const unregister = registerOpenRunningStageSink(sink);
    expect(sink).toHaveBeenCalledWith({ projectId: "proj_1" });
    unregister();
  });
});
