import { render, waitFor } from "@solidjs/testing-library";
import { createEffect, type ComponentProps } from "solid-js";
import { createStore } from "solid-js/store";
import { describe, expect, it, vi } from "vitest";
import { Shell } from "./Shell.tsx";
import { createAppStore } from "../../store/app-state.ts";
import { createShellStore } from "../../store/shell-store.ts";
import { createProjectsStore } from "../../store/projects-store.ts";
import { createRecentsStore } from "../../store/recents-store.ts";
import { createSettingsStore } from "../../store/settings-store.ts";
import { createCostStore } from "../../store/cost-store.ts";
import { createNoticeStore } from "../../notices/notice-store.ts";
import type { WorkspaceSidebar } from "./WorkspaceSidebar.tsx";
import type { WorkspaceStage } from "./WorkspaceStage.tsx";
import type { WorkspaceOverlays } from "./WorkspaceOverlays.tsx";
import type { ChatWorkspace } from "./ChatWorkspace.tsx";
const seams = vi.hoisted(() => ({
  sidebar: null as unknown as ComponentProps<typeof WorkspaceSidebar>,
  stages: new Map<string, ComponentProps<typeof WorkspaceStage>>(),
  overlays: null as unknown as ComponentProps<typeof WorkspaceOverlays>,
  chat: null as unknown as ComponentProps<typeof ChatWorkspace>,
}));
vi.mock("./WorkspaceSidebar.tsx", () => ({ WorkspaceSidebar: (p: typeof seams.sidebar) => { createEffect(() => { seams.sidebar = { ...p }; }); return <div data-testid="sidebar-boundary" />; } }));
vi.mock("./WorkspaceStage.tsx", () => ({ WorkspaceStage: (p: ComponentProps<typeof WorkspaceStage>) => { createEffect(() => { seams.stages.set(p.surfaceKey, { ...p }); }); return <div data-testid={`stage-${p.surfaceKey}`} />; } }));
vi.mock("./WorkspaceOverlays.tsx", () => ({ WorkspaceOverlays: (p: typeof seams.overlays) => { createEffect(() => { seams.overlays = { ...p }; }); return null; } }));
vi.mock("./ChatWorkspace.tsx", () => ({ ChatWorkspace: (p: typeof seams.chat) => { createEffect(() => { seams.chat = { ...p }; }); return <div data-testid="chat-boundary" />; } }));
vi.mock("../../platform/runtime.ts", async (original) => ({ ...(await original<typeof import("../../platform/runtime.ts")>()), isTauriRuntime: () => false, usesCustomWindowChrome: () => false, tauriDragRegionProps: () => ({}) }));
vi.mock("../../platform/connection/app-connection.ts", async (original) => ({ ...(await original<typeof import("../../platform/connection/app-connection.ts")>()),
  getLycaonClient: () => null, onCLIOpenEvent: () => () => undefined, noticeReporterFor: () => ({ reportError: vi.fn(), publish: vi.fn() }) }));
vi.mock("../../contributions/contribution-store.ts", async (original) => ({ ...(await original<typeof import("../../contributions/contribution-store.ts")>()), contributionFrameReady: () => true }));
vi.mock("../../settings/system/onboarding-prefs.ts", () => ({ firstRunSetupCompletedPref: () => true, onboardingPrefsReady: () => true }));
vi.mock("../../platform/windows/window-chrome.ts", async (original) => ({ ...(await original<typeof import("../../platform/windows/window-chrome.ts")>()), windowFocused: () => true, widenWindowBy: vi.fn(async () => "claimed") }));

async function mountShell() {
  seams.stages.clear();
  const appStore = createAppStore(); const shell = createShellStore("connected");
  const projects = createProjectsStore(() => null, { onProjectEvent: () => () => undefined });
  projects.load([{ id: "project", name: "Garden", roots: [{ id: "root", path: "/garden", is_primary: true }] }] as never);
  const recents = createRecentsStore(); await recents.load();
  const [inventory] = createStore({ projectId: "project", loaded: true, rows: [], sort: "recent", rowsSort: "recent", total: 0 });
  const projectSessions = { state: inventory, setProject: vi.fn(), setSort: vi.fn(), refresh: vi.fn(async () => undefined),
    removeRow: vi.fn(), applyRowPatches: vi.fn(), nextPinRank: () => 1, pinnedIds: () => [] };
  appStore.actions.setCurrentSession({ id: "session", project_id: "project", status: "idle", workspace_path: "/garden" } as never);
  appStore.actions.installTranscriptBaseline("session", [], 0);
  shell.commitStageScope({ projectId: "project", sessionId: "session" });
  const view = render(() => <Shell appStore={appStore} shell={shell} projects={projects} recents={recents}
    projectSessions={projectSessions as never} attention={{ index: () => new Map(), state: { rows: [] } } as never}
    notices={createNoticeStore()} settingsStore={createSettingsStore()} costStore={createCostStore()} />);
  await waitFor(() => expect(seams.sidebar).toBeTruthy());
  return { ...view, appStore, shell };
}

describe("shell composition across navigation", () => {
  it("routes workspace-local stages, settings, and overlays while retaining the foreground chat", async () => {
    const { shell } = await mountShell();
    await waitFor(() => expect(seams.chat?.chat.sessionId).toBe("session"));
    expect(seams.chat.projectDirForChat()).toBe("/garden");
    seams.sidebar.goToContextStage("files");
    await waitFor(() => expect(seams.stages.has("stage:project:files")).toBe(true));
    expect(shell.state.foreground?.sessionId).toBe("session");
    seams.sidebar.showStage("chats");
    await waitFor(() => expect(seams.stages.has("chats:project")).toBe(true));
    seams.sidebar.openConfiguration();
    await waitFor(() => expect(seams.stages.has("project-config:project")).toBe(true));
    seams.sidebar.openSettings("general");
    await waitFor(() => expect(seams.stages.has("settings")).toBe(true));
    const settings = seams.stages.get("settings")!;
    settings.openProjectTrustSettings();
    expect(settings.navigation.advancedInitialTab()).toBe("project_trust");
    settings.openExtensionsSettings(); expect(settings.navigation.settingsSection()).toBe("extensions");
    seams.overlays.openKeyboardSettings(); expect(settings.navigation.generalInitialTab()).toBe("keyboard");
    seams.overlays.setLauncherOpen(true); expect(seams.overlays.launcherOpen()).toBe(true);
    seams.overlays.setHelpOpen(true); expect(seams.overlays.helpOpen()).toBe(true);
    seams.sidebar.goHome(); expect(shell.state.activeProjectId).toBeNull();
    await waitFor(() => expect(seams.stages.has("home")).toBe(true));
  });
});
