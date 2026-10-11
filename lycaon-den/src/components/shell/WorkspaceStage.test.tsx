import { render } from "@solidjs/testing-library";
import { createEffect, type ComponentProps } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkspaceStage } from "./WorkspaceStage.tsx";
import { createShellStore } from "../../store/shell-store.ts";
import { SURFACE_HOME, SURFACE_SETTINGS, stageSurfaceKey } from "../../ui/resident-surfaces.ts";
import type { HomeView } from "../home/HomeView.tsx";
import type { SettingsView } from "../settings/SettingsView.tsx";
import type { ProjectChatsView } from "../project/ProjectChatsView.tsx";
import type { ProjectContextView } from "../project/ProjectContextView.tsx";
import type { StageRenderContext } from "../stage/stage-registry.tsx";

const seams = vi.hoisted(() => ({
  home: null as unknown as ComponentProps<typeof HomeView>,
  settings: null as unknown as ComponentProps<typeof SettingsView>,
  chats: null as unknown as ComponentProps<typeof ProjectChatsView>,
  context: null as unknown as ComponentProps<typeof ProjectContextView>,
  stage: null as unknown as StageRenderContext,
  archived: vi.fn(async () => undefined), pinned: vi.fn(async () => undefined), deleted: vi.fn(async () => undefined),
  record: vi.fn(async () => undefined),
}));
vi.mock("../home/HomeView.tsx", () => ({ HomeView: (p: typeof seams.home) => { createEffect(() => { seams.home = { ...p }; }); return null; } }));
vi.mock("../settings/SettingsView.tsx", () => ({ SettingsView: (p: typeof seams.settings) => { createEffect(() => { seams.settings = { ...p }; }); return null; } }));
vi.mock("../project/ProjectChatsView.tsx", () => ({ ProjectChatsView: (p: typeof seams.chats) => { createEffect(() => { seams.chats = { ...p }; }); return null; } }));
vi.mock("../project/ProjectContextView.tsx", () => ({ ProjectContextView: (p: typeof seams.context) => { createEffect(() => { seams.context = { ...p }; }); return null; } }));
vi.mock("../stage/stage-registry.tsx", () => ({ stageDefinition: () => ({ render: (p: typeof seams.stage) => { createEffect(() => { seams.stage = { ...p }; }); return null; } }) }));
vi.mock("../../chat/session/session-retirement.ts", () => ({ setSessionArchived: seams.archived, setSessionPinned: seams.pinned, deleteSession: seams.deleted }));
vi.mock("../../chat/session/session-lifecycle.ts", () => ({ recordCreatedSessionInRecents: seams.record }));
vi.mock("../../platform/runtime.ts", () => ({ isTauriRuntime: () => true }));
vi.mock("../../platform/windows/window-subject.ts", () => ({ windowSubject: () => null }));

type Props = ComponentProps<typeof WorkspaceStage>;
function mountStage(surfaceKey: string) {
  const shell = createShellStore("connected");
  shell.commitStageScope({ projectId: "project", sessionId: "session" });
  const callbacks = {
    navigateFromSearch: vi.fn(async () => undefined), startEmptySession: vi.fn(async () => true),
    openSettings: vi.fn(), openStageWithPlacement: vi.fn((_stage, _arrival, open?: () => void) => open?.()),
    openSearch: vi.fn(), openMoveDialog: vi.fn(), setFoldersPanelProjectId: vi.fn(),
    toggleStar: vi.fn(async () => undefined), renameProject: vi.fn(async () => undefined),
    openProject: vi.fn(), showConversation: vi.fn(async () => true), startOpenFolder: vi.fn(async () => undefined),
    openCloneDialog: vi.fn(), deleteProject: vi.fn(async () => undefined), setHomeSection: vi.fn(),
    selectContextSection: vi.fn(), selectSettingsSection: vi.fn(), openFileInNewWindow: vi.fn(),
    openFileWindow: vi.fn(), detachStageToWindow: vi.fn(async () => undefined), revealInTranscript: vi.fn(),
  };
  const project = { id: "project", name: "  Garden  ", roots: [{ id: "root", label: "Garden", path: "/garden" }], is_draft: false };
  const props = {
    shell, surfaceKey, appStore: {}, projects: { byId: (id: string) => id === "draft" ? { ...project, is_draft: true } : project,
      summary: () => ({ displayName: "Garden" }), state: { projects: [project], registry: {} } }, recents: {},
    activeChat: () => ({ projectId: "project", sessionId: "session" }), attention: { state: { rows: [] } },
    homeSection: () => "recents", homeSummaries: () => [], attentionIndex: () => new Map(), providerGap: () => undefined,
    settingsStore: {}, costStore: {}, navigation: { settingsSection: () => "general", generalInitialTab: () => undefined,
      advancedInitialTab: () => undefined, contextSection: () => "trust", closeSettingsNav: vi.fn(), openContextSection: vi.fn(), ...callbacks },
    sessionNavigation: { sessionRetirementDeps: () => ({ client: "host" }), ...callbacks }, projectLifecycle: callbacks,
    peerWindows: { beginFileWindowDrag: vi.fn(), ...callbacks }, workspaceContext: { backFor: vi.fn(), stageBack: vi.fn() },
    settingsProjectDir: () => "/garden", setSecurityScannersFeature: vi.fn(), openProjectTrustSettings: vi.fn(),
    openExtensionsSettings: vi.fn(), searchArgs: () => ({ originProjectId: "project", seed: "needle", serial: 7 }), ...callbacks,
  } as unknown as Props;
  return { ...render(() => <WorkspaceStage {...props} />), props, callbacks, shell };
}

beforeEach(() => vi.clearAllMocks());
describe("workspace resident stage routing", () => {
  it("keeps Home project lifecycle and idea submission tied to the shell", async () => {
    const { shell, callbacks } = mountStage(SURFACE_HOME);
    seams.home.onShowAllProjects?.(); seams.home.onOpenProviders?.(); seams.home.onOpenDiagnostics?.();
    seams.home.onOpenProject("project"); seams.home.onOpenFolder(); seams.home.onCloneRepo();
    seams.home.onToggleStar("project", true); seams.home.onRename("project", "New name");
    seams.home.onAttachFolder("draft"); seams.home.onAttachFolder("project"); seams.home.onPromote("draft");
    expect(callbacks.openMoveDialog).toHaveBeenCalledWith("draft");
    expect(callbacks.setFoldersPanelProjectId).toHaveBeenCalledWith("project");
    expect(callbacks.openSettings).toHaveBeenCalledWith("debug", { advanced: "diagnostics" });
    expect(callbacks.toggleStar).toHaveBeenCalledWith("project", true);
    expect(callbacks.renameProject).toHaveBeenCalledWith("project", "New name");
    const draft = { text: "Build a garden", attachments: [] };
    expect(await seams.home.onSubmitIdea(draft)).toBe(true);
    expect(callbacks.startEmptySession).toHaveBeenCalledWith({ kind: "new-project" }, { prompt: draft.text, ideaAttachments: [] });
    expect(await seams.home.onSubmitIdea(draft)).toBe(false);
    expect(shell.state.materializingDraft).toBe(true);
    seams.home.onOpenAttention?.({ project_id: "project", session_id: "other" } as never);
    expect(callbacks.navigateFromSearch).toHaveBeenCalledWith({ projectId: "project", sessionId: "other" });
  });

  it("uses the resident chat-list project for host archive, pin, and deletion", async () => {
    const { callbacks } = mountStage("chats:other-project");
    seams.chats.onOpenSession("other-session");
    const row = { id: "other-session", title: "Other" } as never;
    await seams.chats.onSetArchived(row, true); await seams.chats.onSetPinned(row, true); await seams.chats.onDeleteSession(row);
    const target = { projectId: "other-project", sessionId: "other-session", title: "Other" };
    expect(seams.archived).toHaveBeenCalledWith({ client: "host" }, target, true);
    expect(seams.pinned).toHaveBeenCalledWith({ client: "host" }, target, true);
    expect(seams.deleted).toHaveBeenCalledWith({ client: "host" }, target);
    expect(callbacks.navigateFromSearch).toHaveBeenCalledWith({ projectId: "other-project", sessionId: "other-session" });
  });

  it("routes device and project settings without leaking a foreign chat", () => {
    const settings = mountStage(SURFACE_SETTINGS);
    seams.settings.onOpenScanners?.(); seams.settings.onOpenProjectTrust?.();
    expect(settings.callbacks.selectSettingsSection).toHaveBeenCalledWith("scanners");
    expect(settings.callbacks.selectContextSection).toHaveBeenCalledWith("trust");
    settings.unmount();
    const config = mountStage("project-config:other-project");
    expect(seams.context.sessionId).toBeUndefined();
    seams.context.onOpenDeviceSettings?.("providers");
    expect(config.callbacks.openSettings).toHaveBeenCalledWith("providers");
  });

  it("binds registered Files and Search stages to their project and routed callbacks", async () => {
    const files = mountStage(stageSurfaceKey("project", "files"));
    expect(seams.stage.projectName).toBe("Garden"); expect(seams.stage.roots).toHaveLength(1);
    const target = { projectId: "project", sessionId: "other" };
    seams.stage.callbacks.onOpenSession(target); seams.stage.callbacks.onSearchNavigate(target);
    await seams.stage.callbacks.onCreateSupportingWorkflowSession({ projectId: "project", workflow: "recon" } as never);
    expect(files.callbacks.startEmptySession).toHaveBeenCalledWith({ kind: "new-session", projectId: "project" }, { armWorkflow: "recon" });
    seams.stage.callbacks.onOpenCreatedSession(target);
    await Promise.resolve(); await Promise.resolve();
    expect(seams.record).toHaveBeenCalledWith(files.props.recents, target);
    await seams.stage.callbacks.onOpenInNewWindow?.("file-key", { x: 1, y: 2 });
    await seams.stage.callbacks.onOpenInNewWindow?.();
    expect(files.callbacks.openFileInNewWindow).toHaveBeenCalledWith("file-key", { x: 1, y: 2 });
    expect(files.callbacks.detachStageToWindow).toHaveBeenCalledWith("files");
    await seams.stage.callbacks.onOpenFileInNewWindow?.("root", "notes.txt", "Notes");
    expect(files.callbacks.openFileWindow).toHaveBeenCalledWith({ rootId: "root", path: "notes.txt", title: "Notes" });
    seams.stage.callbacks.onOpenSettings("providers");
    seams.stage.callbacks.onOpenSearch?.({ originProjectId: "project", seed: "needle" });
    expect(files.callbacks.openStageWithPlacement).toHaveBeenCalledWith("search", "routed", expect.any(Function));
    expect(files.callbacks.openSearch).toHaveBeenCalledWith({ originProjectId: "project", seed: "needle" });
    files.unmount();
    mountStage(stageSurfaceKey("project", "search"));
    expect(seams.stage.search?.seed).toBe("needle"); expect(seams.stage.search?.serial).toBe(7);
    expect(seams.stage.callbacks.onOpenInNewWindow).toBeUndefined();
  });
});
