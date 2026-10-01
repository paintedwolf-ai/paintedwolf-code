// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { loaded, unloaded } from "../../store/load-state.ts";
import { createShellStore } from "../../store/shell-store.ts";
import {
  attachKnownBackend,
  connectAppBackend,
  disconnectAppBackend,
} from "./app-connection.ts";
import { readSidecarInfo } from "./backend.ts";
import { dismissBootFallback } from "./boot-fallback.ts";
import { firstRunSetupCompletedPref, onboardingPrefsReady } from "../../settings/system/onboarding-prefs.ts";
import { startAppBoot } from "./app-boot.ts";
import { appBootPreparation } from "./app-boot-readiness.ts";
import { applyBootAppStateSnapshot, subscribeAppStateBroadcast } from "../persistence/app-state-sync.ts";
import { loadSharedAppState, getAppStateSnapshot, persistAppState } from "../../store/app-state-snapshot.ts";
import {
  parsePersistedSessionSnapshot,
  clearPersistedLastSessionSnapshot,
  persistedSnapshotMatchesRevision,
} from "../../chat/session/session-chat-persist.ts";
import * as sessionSwitch from "../../chat/session/session-switch.ts";
import * as sessionCache from "../../chat/session/session-chat-cache.ts";
import { cachedToProject } from "../../store/boot-cache-model.ts";
import type { CachedProject } from "../../../shared/app-state-types.ts";
import { watchWindowExitForHotExit } from "../../files/documents/files-hot-exit-window.ts";
import { watchWindowExitForFilesTreeView } from "../../files/tree/files-tree-view-window.ts";
import { watchWindowExitForAppState } from "../persistence/app-state-window.ts";

vi.mock("./app-connection.ts", () => ({
  connectAppBackend: vi.fn().mockResolvedValue({}),
  disconnectAppBackend: vi.fn(),
  getLycaonClient: vi.fn().mockReturnValue({}),
  attachKnownBackend: vi.fn(),
  handleSessionGone: vi.fn(),
}));

vi.mock("./backend.ts", () => ({
  readSidecarInfo: vi.fn().mockResolvedValue(null),
}));

vi.mock("./boot-fallback.ts", () => ({
  dismissBootFallback: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("../../store/app-state-snapshot.ts", () => ({
  loadSharedAppState: vi.fn().mockResolvedValue({ version: 1, recents: [] }),
  getAppStateSnapshot: vi.fn().mockReturnValue({ version: 1, recents: [] }),
  persistAppState: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("../../chat/session/session-chat-persist.ts", () => ({
  parsePersistedSessionSnapshot: vi.fn().mockReturnValue(null),
  persistedSnapshotMatchesRevision: vi.fn().mockReturnValue(true),
  seedSessionChatCacheFromPersisted: vi.fn().mockReturnValue(null),
  clearPersistedLastSessionSnapshot: vi.fn().mockResolvedValue(undefined),
  toSessionChatSnapshot: vi.fn((snap: unknown) => snap),
}));

vi.mock("../persistence/app-state-sync.ts", () => ({
  applyBootAppStateSnapshot: vi.fn(),
  subscribeAppStateBroadcast: vi.fn(() => () => {}),
}));

vi.mock("../../files/documents/files-hot-exit-window.ts", () => ({
  watchWindowExitForHotExit: vi.fn(),
}));

vi.mock("../../files/tree/files-tree-view-window.ts", () => ({
  watchWindowExitForFilesTreeView: vi.fn(),
}));

vi.mock("../persistence/app-state-window.ts", () => ({
  watchWindowExitForAppState: vi.fn(),
}));

vi.mock("../../files/tree/files-tree-view-state.ts", () => ({
  resolveFilesTreeWindowLabel: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("../../chat/stream/transcript-viewport-state.ts", () => ({
  resolveTranscriptViewportWindowLabel: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("./health.ts", () => ({
  lastSeenStoreRevision: vi.fn().mockReturnValue(1),
}));

vi.mock("../../settings/system/debug-prefs.ts", () => ({
  fullDebugLoggingPref: vi.fn(() => false),
}));

vi.mock("../../chat/stream/den-main-thread-perf.ts", () => ({
  installMainThreadPerfObserver: vi.fn(),
}));

vi.mock("../../settings/system/onboarding-prefs.ts", () => ({
  firstRunSetupCompletedPref: vi.fn(() => true),
  onboardingPrefsReady: vi.fn(() => true),
}));

describe("startAppBoot", () => {
  it("registers preservation before the initial durable-state read settles", async () => {
    let finish!: () => void;
    vi.mocked(loadSharedAppState).mockImplementationOnce(() => new Promise((resolve) => {
      finish = () => resolve({ version: 1, recents: [] } as never);
    }));
    const boot = startAppBoot({
      appStore: { actions: { setSidecarStatus: vi.fn() } } as never,
      projects: { hydrate: vi.fn().mockResolvedValue(undefined), state: { registry: loaded(null), projects: [] } } as never,
      recents: { load: vi.fn().mockResolvedValue(undefined), state: { recents: [] } } as never,
      shell: createShellStore("offline"),
    });
    expect(watchWindowExitForHotExit).toHaveBeenCalledOnce();
    expect(watchWindowExitForFilesTreeView).toHaveBeenCalledOnce();
    expect(watchWindowExitForAppState).toHaveBeenCalledOnce();
    finish();
    await boot;
  });
  it.each(["connected", "offline"])("holds sends through splash dismissal and backend reconciliation: %s", async (outcome) => {
    let dismiss!: () => void;
    let connect!: () => void;
    vi.mocked(dismissBootFallback).mockImplementationOnce(() => new Promise<void>((resolve) => { dismiss = resolve; }));
    vi.mocked(connectAppBackend).mockImplementationOnce(async () => {
      await new Promise<void>((resolve) => { connect = resolve; });
      if (outcome === "offline") throw new Error("offline");
      return {} as never;
    });
    const boot = startAppBoot({
      appStore: { actions: { setSidecarStatus: vi.fn() } } as never,
      projects: { hydrate: vi.fn().mockResolvedValue(undefined), state: { registry: loaded(null), projects: [] } } as never,
      recents: { load: vi.fn().mockResolvedValue(undefined), state: { recents: [] } } as never,
      shell: createShellStore("offline"),
    });
    expect(appBootPreparation.ready()).toBe(false);
    await vi.waitFor(() => expect(dismiss).toBeTypeOf("function"));
    expect(appBootPreparation.ready()).toBe(false);
    dismiss();
    await boot;
    expect(appBootPreparation.ready()).toBe(false);
    connect();
    await vi.waitFor(() => expect(appBootPreparation.ready()).toBe(true));
  });
  afterEach(() => {
    vi.restoreAllMocks();
    vi.clearAllMocks();
    vi.mocked(persistedSnapshotMatchesRevision).mockReturnValue(true);
    vi.mocked(persistAppState).mockResolvedValue(undefined);
    vi.mocked(getAppStateSnapshot).mockReturnValue({ version: 1, recents: [] } as never);
    vi.mocked(firstRunSetupCompletedPref).mockReturnValue(true);
    vi.mocked(onboardingPrefsReady).mockReturnValue(true);
  });

  it.each([true, false])("revalidates the saved project after a host revision change (exists=%s)", async (exists) => {
    const scope = { projectId: "restart-project", sessionId: "saved-chat" };
    const bootState = {
      version: 1 as const, recents: [], lastActiveProjectId: scope.projectId,
      lastSessionSnapshot: { scope },
    };
    vi.mocked(getAppStateSnapshot).mockReturnValue(bootState as never);
    vi.mocked(persistAppState).mockImplementation(async (patch) => { Object.assign(bootState, patch); });
    vi.mocked(parsePersistedSessionSnapshot).mockReturnValueOnce({ scope } as never);
    vi.mocked(persistedSnapshotMatchesRevision).mockReturnValue(false);
    const dropCache = vi.spyOn(sessionCache, "dropSessionChatCache");
    const resume = vi.spyOn(sessionSwitch, "runResumeSession").mockImplementation(async ({ scope: resumed, deps }) => {
      expect(clearPersistedLastSessionSnapshot).toHaveBeenCalled();
      deps.openSession(resumed);
    });
    let connected = false;
    vi.mocked(connectAppBackend).mockImplementationOnce(async () => {
      connected = true;
      return {} as never;
    });
    const shell = createShellStore("offline");
    const resetChatForSessionSwitch = vi.fn();
    await startAppBoot({
      appStore: { actions: {
        setSidecarStatus: vi.fn(), restoreSessionChatSnapshot: vi.fn(), resetChatForSessionSwitch,
      } } as never,
      projects: {
        seedFromSnapshot: vi.fn(),
        byId: (id: string) => id === scope.projectId && (!connected || exists) ? { id } : undefined,
        state: { registry: loaded(null), projects: [] },
      } as never,
      recents: {
        load: vi.fn().mockResolvedValue(undefined),
        state: { recents: [{ projectId: "another-project", sessionId: "another-chat" }, scope] },
      } as never,
      shell,
    });
    await vi.waitFor(() => expect(resetChatForSessionSwitch).toHaveBeenCalled());
    if (exists) {
      await vi.waitFor(() => expect(resume).toHaveBeenCalled());
      expect(shell.state.foreground).toEqual(scope);
      expect(bootState.lastActiveProjectId).toBe(scope.projectId);
      expect(dropCache).toHaveBeenCalledWith(scope);
    } else {
      await vi.waitFor(() => expect(shell.state.activeProjectId).toBeNull());
      expect(resume).not.toHaveBeenCalled();
      expect(bootState.lastActiveProjectId).toBeUndefined();
    }
  });

  it("hydrates stores and dismisses the splash after bootstrap", async () => {
    const recents = {
      load: vi.fn().mockResolvedValue(undefined),
      state: { recents: [] },
    };
    const projects = {
      seedFromSnapshot: vi.fn(),
      hydrate: vi.fn().mockResolvedValue(undefined),
      state: { registry: loaded(null), projects: [] },
    };
    const shell = createShellStore("offline");
    const appStore = { actions: { setSidecarStatus: vi.fn() } } as never;

    await startAppBoot({
      appStore,
      projects: projects as never,
      recents: recents as never,
      shell,
    });

    expect(recents.load).toHaveBeenCalledOnce();
    expect(dismissBootFallback).toHaveBeenCalledOnce();
    await vi.waitFor(() => expect(disconnectAppBackend).toHaveBeenCalledOnce());
    await vi.waitFor(() => expect(projects.hydrate).not.toHaveBeenCalled());
  });

  it("dismisses the splash before backend connect finishes", async () => {
    let releaseConnect!: () => void;
    const connectGate = new Promise<void>((resolve) => {
      releaseConnect = () => resolve();
    });
    vi.mocked(connectAppBackend).mockImplementation(async () => {
      await connectGate;
      return {} as never;
    });

    const recents = {
      load: vi.fn().mockResolvedValue(undefined),
      state: { recents: [] },
    };
    const projects = {
      hydrate: vi.fn().mockResolvedValue(undefined),
      state: { registry: loaded(null), projects: [] },
    };
    const shell = createShellStore("offline");
    const appStore = { actions: { setSidecarStatus: vi.fn() } } as never;

    await startAppBoot({
      appStore,
      projects: projects as never,
      recents: recents as never,
      shell,
    });

    expect(dismissBootFallback).toHaveBeenCalledOnce();
    expect(connectAppBackend).toHaveBeenCalledOnce();
    expect(projects.hydrate).not.toHaveBeenCalled();

    releaseConnect();
    await vi.waitFor(() => expect(connectAppBackend).toHaveBeenCalledOnce());
    expect(projects.hydrate).not.toHaveBeenCalled();
  });

  it("holds the brand splash for unlatched first-run until Shell reveals the gate", async () => {
    vi.mocked(firstRunSetupCompletedPref).mockReturnValue(false);
    vi.mocked(onboardingPrefsReady).mockReturnValue(true);

    const recents = {
      load: vi.fn().mockResolvedValue(undefined),
      state: { recents: [] },
    };
    const projects = {
      hydrate: vi.fn().mockResolvedValue(undefined),
      state: { registry: loaded(null), projects: [] },
    };
    const shell = createShellStore("offline");
    const appStore = { actions: { setSidecarStatus: vi.fn() } } as never;

    await startAppBoot({
      appStore,
      projects: projects as never,
      recents: recents as never,
      shell,
    });

    expect(applyBootAppStateSnapshot).toHaveBeenCalledOnce();
    expect(dismissBootFallback).not.toHaveBeenCalled();
  });

  it("reveals the app even when local bootstrap throws", async () => {
    const recents = {
      load: vi.fn().mockRejectedValue(new Error("recents failed")),
    };
    const projects = { hydrate: vi.fn().mockResolvedValue(undefined) };
    const shell = createShellStore("offline");
    const appStore = { actions: { setSidecarStatus: vi.fn() } } as never;

    await expect(
      startAppBoot({
        appStore,
        projects: projects as never,
        recents: recents as never,
        shell,
      }),
    ).rejects.toThrow("recents failed");

    // Dismissing the splash reveals the root after boot failure.
    expect(dismissBootFallback).toHaveBeenCalledOnce();
    expect(projects.hydrate).not.toHaveBeenCalled();
  });

  it("hydrates projects and clears home when boot scope references a missing project", async () => {
    vi.mocked(connectAppBackend).mockRejectedValueOnce(new Error("sidecar down"));

    const cached: CachedProject = {
      id: "gone-project",
      name: "Ghost",
      roots: [],
      roots_generation: 0,
      session_count: 0,
      starred: false,
      is_draft: false,
      promotion: null,
      last_activity_at: null,
      last_opened_at: "2026-01-01T00:00:00Z",
      created_at: "2026-01-01T00:00:00Z",
    };
    const bootState = {
      version: 1 as const,
      recents: [],
      lastActiveProjectId: "gone-project",
      cachedProjects: [cached],
      lastSessionSnapshot: { scope: { projectId: "gone-project", sessionId: "sess-1" } },
    };
    vi.mocked(loadSharedAppState).mockResolvedValueOnce(bootState as never);
    vi.mocked(getAppStateSnapshot).mockReturnValue(bootState as never);
    vi.mocked(parsePersistedSessionSnapshot).mockReturnValueOnce({
      scope: { projectId: "gone-project", sessionId: "sess-1" },
      session: {
        id: "sess-1",
        project_id: "gone-project",
        workspace_path: "/tmp/ghost",
      },
      messages: [],
      touchedAt: Date.now(),
      transcriptWatermark: 0,
      workers: [],
      workerTranscripts: {},
      workflowRuns: [],
      workflowCatalog: [],
      blueprints: [],
      pendingCheckpoints: [],
      storeRevisionAtPersist: 1,
    } as never);

    const shell = createShellStore("offline");
    const resetChatForSessionSwitch = vi.fn();
    const restoreSessionChatSnapshot = vi.fn();
    const recents = { load: vi.fn().mockResolvedValue(undefined) };

    const projectsState = {
      registry: unloaded<null>(),
      projects: [cachedToProject(cached)],
    };
    const projects = {
      seedFromSnapshot: vi.fn((rows: readonly CachedProject[]) => {
        projectsState.projects = rows.map(cachedToProject);
      }),
      hydrate: vi.fn(async () => {
        projectsState.projects = [];
        projectsState.registry = loaded(null);
      }),
      byId: vi.fn((id: string) =>
        projectsState.projects.find((project) => project.id === id),
      ),
      state: projectsState,
    };

    await startAppBoot({
      appStore: {
        actions: {
          resetChatForSessionSwitch,
          restoreSessionChatSnapshot,
          setSidecarStatus: vi.fn(),
        },
      } as never,
      projects: projects as never,
      recents: recents as never,
      shell,
    });

    await vi.waitFor(() => expect(projects.hydrate).toHaveBeenCalledOnce());
    await vi.waitFor(() => expect(resetChatForSessionSwitch).toHaveBeenCalledOnce());
    expect(shell.state.activeProjectId).toBeNull();
    expect(persistAppState).toHaveBeenCalledWith({ lastActiveProjectId: undefined });
    expect(clearPersistedLastSessionSnapshot).toHaveBeenCalled();
  });

  it("still marks projects loaded when backend connect fails at boot", async () => {
    vi.mocked(connectAppBackend).mockRejectedValueOnce(new Error("sidecar down"));

    const recents = { load: vi.fn().mockResolvedValue(undefined) };
    const hydrate = vi.fn().mockResolvedValue(undefined);
    const projects = {
      seedFromSnapshot: vi.fn(),
      hydrate,
      byId: vi.fn(),
      state: { registry: unloaded(), projects: [] },
    };
    const shell = createShellStore("offline");
    const appStore = { actions: { setSidecarStatus: vi.fn() } } as never;

    await startAppBoot({
      appStore,
      projects: projects as never,
      recents: recents as never,
      shell,
    });

    await vi.waitFor(() => expect(hydrate).toHaveBeenCalledOnce());
  });

  it("hydrates the project registry for an attached item window", async () => {
    vi.mocked(readSidecarInfo).mockResolvedValueOnce({
      baseUrl: "http://127.0.0.1:1234",
      apiToken: "test-token",
    });
    const recents = {
      load: vi.fn().mockResolvedValue(undefined),
      state: { recents: [] },
    };
    const projects = {
      seedFromSnapshot: vi.fn(),
      hydrate: vi.fn().mockResolvedValue(undefined),
      byId: vi.fn(),
      state: { registry: loaded(null), projects: [] },
    };

    await startAppBoot({
      appStore: { actions: { setSidecarStatus: vi.fn() } } as never,
      projects: projects as never,
      recents: recents as never,
      shell: createShellStore("offline"),
      attachOnly: true,
    });

    await vi.waitFor(() => expect(attachKnownBackend).toHaveBeenCalledOnce());
    expect(connectAppBackend).not.toHaveBeenCalled();
    expect(projects.hydrate).toHaveBeenCalledOnce();
  });

  it("opens an attached Context window at its requested project without a session", async () => {
    vi.mocked(readSidecarInfo).mockResolvedValueOnce({
      baseUrl: "http://127.0.0.1:1234",
      apiToken: "test-token",
    });
    const shell = createShellStore("offline");
    const projects = {
      seedFromSnapshot: vi.fn(),
      hydrate: vi.fn().mockResolvedValue(undefined),
      byId: vi.fn((id: string) => (id === "project-1" ? {} : undefined)),
      state: { registry: loaded(null), projects: [] },
    };

    await startAppBoot({
      appStore: { actions: { setSidecarStatus: vi.fn() } } as never,
      projects: projects as never,
      recents: {
        load: vi.fn().mockResolvedValue(undefined),
        state: { recents: [] },
      } as never,
      shell,
      requestedProjectId: "project-1",
      attachOnly: true,
    });

    await vi.waitFor(() =>
      expect(shell.state.activeProjectId).toBe("project-1"),
    );
    expect(shell.state.foreground).toBeNull();
  });

  it("projects the persisted snapshot at boot and subscribes for broadcasts", async () => {
    const recents = {
      load: vi.fn().mockResolvedValue(undefined),
      state: { recents: [] },
    };
    const projects = {
      seedFromSnapshot: vi.fn(),
      hydrate: vi.fn().mockResolvedValue(undefined),
      byId: vi.fn(),
      state: { registry: loaded(null), projects: [] },
    };
    const shell = createShellStore("offline");
    const appStore = { actions: { setSidecarStatus: vi.fn() } } as never;

    await startAppBoot({
      appStore,
      projects: projects as never,
      recents: recents as never,
      shell,
    });

    expect(applyBootAppStateSnapshot).toHaveBeenCalledOnce();
    expect(subscribeAppStateBroadcast).toHaveBeenCalledOnce();
  });
});
