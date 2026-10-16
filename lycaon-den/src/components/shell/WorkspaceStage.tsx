import { createShellWorkspaceContext } from "./shell-workspace-context.ts";
import { createShellNavigationState } from "./shell-navigation-state.ts";
import { createShellOnboarding } from "./shell-onboarding.tsx";
import { createShellWorkerDrawer } from "./shell-worker-drawer.ts";
import { createShellSearchNavigation } from "./shell-search-navigation.ts";
import { createShellSearchState } from "./shell-search-state.ts";
import { type ShellScope } from "./shell-scope.ts";
import { createShellPeerWindows } from "./shell-peer-windows.ts";
import { createSessionNavigation } from "./session-navigation.ts";
import { createProjectLifecycle } from "./project-lifecycle.ts";
import { createProjectRemoval } from "./project-removal.ts";
import { CONTEXT_NAV_CATALOG, type ContextNavItemId } from "../../../shared/app-state-types.ts";
import { recordCreatedSessionInRecents } from "../../chat/session/session-lifecycle.ts";
import {
  deleteSession,
  setSessionArchived,
  setSessionPinned,
} from "../../chat/session/session-retirement.ts";
import { type HomeSection } from "../../home/home-model.ts";
import {
  cancelItemWindowDrag,
  closeCurrentPeerWindow,
  finishItemWindowDrag,
  moveItemWindowDrag,
} from "../../platform/windows/item-windows.ts";
import { isTauriRuntime } from "../../platform/runtime.ts";
import { windowSubject } from "../../platform/windows/window-subject.ts";
import { type ProjectSummary } from "../../project/project-summary.ts";
import { type ProjectContextSection } from "../../settings/settings-nav-model.ts";
import { type AttentionStore } from "../../store/attention-store.ts";
import { type CostStore } from "../../store/cost-store.ts";
import { type SettingsStore } from "../../store/settings-store.ts";
import {
  SURFACE_HOME,
  SURFACE_SETTINGS,
  parseStageSurfaceKey,
} from "../../ui/resident-surfaces.ts";
import { HomeView } from "../home/HomeView.tsx";
import {
  clearFilesHotExitSuppress,
  suppressFilesHotExitWrite,
} from "../../files/documents/files-hot-exit.ts";
import { projectFilesState } from "../../files/documents/files-buffer-state.ts";
import { ProjectChatsView } from "../project/ProjectChatsView.tsx";
import { ProjectContextView } from "../project/ProjectContextView.tsx";
import { SettingsView } from "../settings/SettingsView.tsx";
import { stageDefinition } from "../stage/stage-registry.tsx";

type Props = Pick<ShellScope, "shell" | "appStore" | "projects" | "recents" | "activeChat"> & {
  surfaceKey: string;
  settingsStore: SettingsStore;
  costStore: CostStore;
  attention: AttentionStore;
  homeSection: () => HomeSection;
  setHomeSection: (section: HomeSection) => void;
  homeSummaries: () => ProjectSummary[];
  attentionIndex: () => ReturnType<AttentionStore["index"]>;
  navigation: Pick<
    ReturnType<typeof createShellNavigationState>,
    | "settingsSection"
    | "generalInitialTab"
    | "advancedInitialTab"
    | "contextSection"
    | "openSettings"
    | "selectSettingsSection"
    | "openContextSection"
    | "closeSettingsNav"
  >;
  searchArgs: ReturnType<typeof createShellSearchState>["searchArgs"];
  navigateFromSearch: ReturnType<typeof createShellSearchNavigation>["navigateFromSearch"];
  sessionNavigation: Pick<
    ReturnType<typeof createSessionNavigation>,
    | "startEmptySession"
    | "sessionRetirementDeps"
    | "openProject"
  >;
  projectLifecycle: Pick<ReturnType<typeof createProjectLifecycle>, "toggleStar" | "renameProject" | "openMoveDialog">;
  peerWindows: Pick<
    ReturnType<typeof createShellPeerWindows>,
    | "openFileInNewWindow"
    | "openFileWindow"
    | "beginFileWindowDrag"
    | "detachStageToWindow"
  >;
  workspaceContext: Pick<ReturnType<typeof createShellWorkspaceContext>, "backFor" | "stageBack">;
  providerGap: ReturnType<typeof createShellOnboarding>["providerGap"];
  showConversation: () => Promise<unknown>;
  focusComposer: () => void;
  startOpenFolder: () => Promise<unknown>;
  openCloneDialog: () => void;
  deleteProject: ReturnType<typeof createProjectRemoval>;
  setFoldersPanelProjectId: (id: string) => void;
  settingsProjectDir: () => string | undefined;
  setSecurityScannersFeature: (enabled: boolean) => void;
  selectContextSection: (section: ProjectContextSection) => void;
  openProjectTrustSettings: () => void;
  openExtensionsSettings: () => void;
  openStageWithPlacement: (stage: ContextNavItemId, arrival: "routed" | "deliberate", open?: () => void) => void;
  openSearch: ReturnType<typeof createShellSearchState>["openSearch"];
  revealInTranscript: ReturnType<typeof createShellWorkerDrawer>["revealInTranscript"];
};

export function WorkspaceStage(props: Props) {
  const {
    homeSection,
    setHomeSection,
    homeSummaries,
    attentionIndex,
    providerGap,
    showConversation,
    startOpenFolder,
    openCloneDialog,
    deleteProject,
    setFoldersPanelProjectId,
    settingsProjectDir,
    setSecurityScannersFeature,
    selectContextSection,
    openProjectTrustSettings,
    openExtensionsSettings,
    openStageWithPlacement,
    openSearch,
    revealInTranscript,
    searchArgs,
    navigateFromSearch,
    activeChat,
  } = props;
  const {
    settingsSection,
    generalInitialTab,
    advancedInitialTab,
    contextSection,
    openSettings,
    selectSettingsSection,
    openContextSection,
    closeSettingsNav,
  } = props.navigation;
  const { startEmptySession, sessionRetirementDeps, openProject } = props.sessionNavigation;
  const { toggleStar, renameProject, openMoveDialog } = props.projectLifecycle;
  const {
    openFileInNewWindow,
    openFileWindow,
    beginFileWindowDrag,
    detachStageToWindow,
  } = props.peerWindows;
  const { backFor, stageBack } = props.workspaceContext;
  const openProvidersSettings = () => openSettings("providers");
  const itemSubject = windowSubject();
  const ProjectStage = (stageProps: { stage: ContextNavItemId }) => {
    const stage = stageProps.stage;
    const projectId = props.shell.state.activeProjectId;
    if (stage !== "search" && !projectId) return null;
    const args = stage === "search" ? searchArgs() : undefined;
    const pid = projectId ?? args?.originProjectId ?? null;
    const definition = stageDefinition(stage);
    return definition.render({
      projectId: pid,
      get projectName() {
        return (pid ? props.projects.byId(pid)?.name?.trim() : undefined) || "Untitled project";
      },
      appStore: props.appStore,
      get roots() {
        return (pid ? props.projects.byId(pid)?.roots : undefined) ?? [];
      },
      get projects() {
        return props.projects.state.projects;
      },
      back: () => stageBack(stage),
      search: {
        get originProjectId() {
          return (
            props.shell.state.activeProjectId ??
            searchArgs()?.originProjectId ??
            null
          );
        },
        get seed() {
          return searchArgs()?.seed;
        },
        get replaceMode() {
          return searchArgs()?.replaceMode;
        },
        get serial() {
          return searchArgs()?.serial ?? 0;
        },
      },
      callbacks: {
        onOpenSession: (target) => void navigateFromSearch(target),
        onOpenCreatedSession: (target) => {
          void (async () => {
            await recordCreatedSessionInRecents(props.recents, target);
            await navigateFromSearch(target);
          })();
        },
        onCreateSupportingWorkflowSession: async ({ projectId, workflow }) => {
          await startEmptySession(
            { kind: "new-session", projectId },
            { armWorkflow: workflow },
          );
        },
        onOpenSettings: (section) => openSettings(section),
        onSearchNavigate: (target) => void navigateFromSearch(target),
        onOpenInNewWindow:
          stage === "files" &&
          pid &&
          isTauriRuntime()
            ? (key, position) =>
                key
                  ? openFileInNewWindow(key, position)
                  : void detachStageToWindow("files")
            : undefined,
        onOpenFileInNewWindow:
          stage === "files" && pid && isTauriRuntime()
            ? (rootId, path, title) =>
                openFileWindow({ rootId, path, title })
            : undefined,
        fileTabWindowDrag:
          stage === "files" && pid && isTauriRuntime()
            ? {
                begin: beginFileWindowDrag,
                move: moveItemWindowDrag,
                finish: finishItemWindowDrag,
                cancel: cancelItemWindowDrag,
            }
            : undefined,
        restoreFilesHotExit: itemSubject?.kind !== "file",
        onFileTabMovedOut:
          stage === "files" && pid && itemSubject?.kind === "file"
            ? () => {
                if (projectFilesState(pid).order.length > 0) return;
                suppressFilesHotExitWrite(pid);
                void closeCurrentPeerWindow().catch((err) => {
                  clearFilesHotExitSuppress(pid);
                  console.debug("[item-window] close empty file window failed", err);
                });
              }
            : undefined,
        onRevealInTranscript: (target) => revealInTranscript(pid, target),
        onOpenSearch: (args) =>
          openStageWithPlacement("search", "routed", () =>
            openSearch({ originProjectId: args.originProjectId, seed: args.seed })),
      },
    });
  };

  const key = props.surfaceKey;
  if (key === SURFACE_HOME) {
    return (
      <HomeView
        summaries={homeSummaries()}
        section={homeSection()}
        onShowAllProjects={() => setHomeSection("all")}
        registry={props.projects.state.registry}
        providerGap={providerGap()}
        onOpenProviders={openProvidersSettings}
        onOpenDiagnostics={() => openSettings("debug", { advanced: "diagnostics" })}
        onSubmitIdea={async (draft) => {
          if (!props.shell.beginDraftMaterialization()) return false;
          void showConversation();
          return (await startEmptySession(
            { kind: "new-project" },
            { prompt: draft.text, ideaAttachments: draft.attachments },
          )) === true;
        }}
        submitting={props.shell.state.materializingDraft}
        onOpenFolder={() => void startOpenFolder()}
        onCloneRepo={() => openCloneDialog()}
        onOpenProject={(projectId) => openProject(projectId)}
        onToggleStar={(id, starred) => void toggleStar(id, starred)}
        onRename={(id, name) => void renameProject(id, name)}
        onAttachFolder={(id) => {
          if (props.projects.byId(id)?.is_draft) openMoveDialog(id);
          else setFoldersPanelProjectId(id);
        }}
        onPromote={(id) => openMoveDialog(id)}
        onDelete={(id, opts) => void deleteProject(id, opts)}
        attentionRows={props.attention.state.rows}
        onOpenAttention={(row) =>
          void navigateFromSearch({
            projectId: row.project_id,
            sessionId: row.session_id,
          }).then(() => props.focusComposer())
        }
      />
    );
  }
  if (key === SURFACE_SETTINGS) {
    return (
      <SettingsView
        back={backFor("settings", "settings-back")}
        section={settingsSection()}
        generalInitialTab={generalInitialTab()}
        advancedInitialTab={advancedInitialTab()}
        appStore={props.appStore}
        settingsStore={props.settingsStore}
        costStore={props.costStore}
        projects={props.projects}
        projectDir={settingsProjectDir()}
        projectName={
          props.projects.summary(
            props.shell.state.activeProjectId ?? "",
          )?.displayName
        }
        onClose={closeSettingsNav}
        onOpenScanners={() => selectSettingsSection("scanners")}
        onOpenProjectSettings={
          props.shell.state.activeProjectId ? openContextSection : undefined
        }
        onOpenProjectTrust={
          props.shell.state.activeProjectId
            ? () => selectContextSection("trust")
            : undefined
        }
        onSecurityScannersFeatureChange={setSecurityScannersFeature}
      />
    );
  }
  if (key.startsWith("chats:")) {
    const projectId = key.slice("chats:".length);
    if (!projectId) return null;
    return (
      <ProjectChatsView
        projectId={projectId}
        appStore={props.appStore}
        back={backFor("chats", "chats-back")}
        attention={attentionIndex()}
        onOpenSession={(sessionId) =>
          void navigateFromSearch({ projectId, sessionId })
        }
        onSetArchived={(row, archived) =>
          setSessionArchived(
            sessionRetirementDeps(),
            { projectId, sessionId: row.id, title: row.title },
            archived,
          )
        }
        onSetPinned={(row, pinned) =>
          setSessionPinned(
            sessionRetirementDeps(),
            { projectId, sessionId: row.id, title: row.title },
            pinned,
          )
        }
        onDeleteSession={(row) =>
          deleteSession(sessionRetirementDeps(), {
            projectId,
            sessionId: row.id,
            title: row.title,
          })
        }
      />
    );
  }
  if (key.startsWith("project-config:")) {
    const projectId = key.slice("project-config:".length);
    if (!projectId) return null;
    return (
      <ProjectContextView
        back={backFor("context", "context-back")}
        section={contextSection()}
        onSectionChange={selectContextSection}
        projectId={projectId}
        sessionId={activeChat()?.projectId === projectId ? activeChat()?.sessionId : undefined}
        projectDir={settingsProjectDir()}
        appStore={props.appStore}
        settingsStore={props.settingsStore}
        projects={props.projects}
        onClose={closeSettingsNav}
        onOpenDeviceTrust={openProjectTrustSettings}
        onOpenExtensionsSettings={openExtensionsSettings}
        onOpenDeviceSettings={(section) => openSettings(section)}
      />
    );
  }
  const stage = parseStageSurfaceKey(key);
  if (!stage || !(CONTEXT_NAV_CATALOG as readonly string[]).includes(stage)) {
    return null;
  }
  const id = stage as ContextNavItemId;
  if (id === "files") {
    return (
      <div class="den-resident-stage-content" data-boot="ready">
        <ProjectStage stage={id} />
      </div>
    );
  }
  return <ProjectStage stage={id} />;
}
