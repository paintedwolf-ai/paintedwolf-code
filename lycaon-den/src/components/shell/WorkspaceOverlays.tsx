import { createShellSearchNavigation } from "./shell-search-navigation.ts";
import { createShellSearchState } from "./shell-search-state.ts";
import { shellSearchTargets } from "./shell-search-targets.ts";
import { type ShellScope } from "./shell-scope.ts";
import { createShellPeerWindows } from "./shell-peer-windows.ts";
import { createShellContributions } from "./shell-contributions.ts";
import { Show } from "solid-js";
import { EditorCommandBar } from "../../find/EditorCommandBar.tsx";
import { editorCommandBarPlacement } from "../../find/find-controller.ts";
import { type HomeSection } from "../../home/home-model.ts";
import { diffViewerRequest } from "../../platform/navigation/in-app-diff.ts";
import { closeItemWindow } from "../../platform/windows/item-windows.ts";
import { resolveSourceRequest } from "../../platform/navigation/open-source.ts";
import { focusEditorWindow } from "../../platform/windows/editor-windows.ts";
import { searchHitToNavTarget } from "../../search/search-hit-nav.ts";
import { openSearchHit } from "../../search/search-hit-open.ts";
import { type CrossbarGotoTarget } from "../../search/crossbar-model.ts";
import { FirstTimeTipsLayer } from "../first-time-tips/FirstTimeTipsLayer.tsx";
import { ProjectLauncher } from "../project/ProjectLauncher.tsx";
import { Crossbar } from "../search/Crossbar.tsx";
import { ShortcutHelpOverlay } from "../shortcuts/ShortcutHelpOverlay.tsx";
import { DiffViewerDrawer } from "../source/diff/DiffViewerDrawer.tsx";
import { PeerViewSwitcher, type PeerViewSwitcherHandle } from "./PeerViewSwitcher.tsx";
import { createProjectOpening } from "./project-opening.ts";
import { RecentChatSwitcher, type RecentChatSwitcherHandle } from "./RecentChatSwitcher.tsx";
import { createShellAttention } from "./shell-attention.ts";

type Props = Pick<
    ShellScope,
    | "shell"
    | "appStore"
    | "projects"
    | "launcherOpen"
    | "setLauncherOpen"
    | "helpOpen"
    | "setHelpOpen"
  > & {
  openProject: (id: string) => void;
  newProject: () => void;
  goHome: () => void;
  setHomeSection: (section: HomeSection) => void;
  search: Pick<
    ReturnType<typeof createShellSearchState>,
    | "crossbarOpen"
    | "crossbarArgs"
    | "closeCrossbar"
    | "escalateCrossbarToSearch"
  >;
  crossbarGotoTargets: () => ReturnType<typeof shellSearchTargets>;
  crossbarActions: ReturnType<typeof createShellContributions>["crossbarActions"];
  navigateFromCrossbarGoto: (target: CrossbarGotoTarget) => void;
  navigateFromSearch: ReturnType<typeof createShellSearchNavigation>["navigateFromSearch"];
  proposeFolderAsProject: ReturnType<typeof createProjectOpening>["proposeFolderAsProject"];
  recentChatEntries: ReturnType<typeof createShellAttention>["recentChatEntries"];
  recentSwitcherRef: (handle: RecentChatSwitcherHandle) => void;
  peerViewSwitcherRef: (handle: PeerViewSwitcherHandle) => void;
  focusedPeerViews: ReturnType<typeof createShellPeerWindows>["focusedPeerViews"];
  openKeyboardSettings: () => void;
  navSlotWidthCss: () => string;
};

export function WorkspaceOverlays(props: Props) {
  const {
    launcherOpen,
    setLauncherOpen,
    helpOpen,
    setHelpOpen,
    openProject,
    newProject,
    goHome,
    setHomeSection,
    crossbarGotoTargets,
    crossbarActions,
    navigateFromCrossbarGoto,
    navigateFromSearch,
    proposeFolderAsProject,
    recentChatEntries,
    focusedPeerViews,
    openKeyboardSettings,
    navSlotWidthCss,
  } = props;
  const { crossbarOpen, crossbarArgs, closeCrossbar, escalateCrossbarToSearch } = props.search;
  return <>
      <Show when={editorCommandBarPlacement() === "shell"}>
        <div
          class="den-find-bar-host"
          data-testid="find-bar-host"
          style={{ "--den-nav-slot-width": navSlotWidthCss() }}
        >
          <EditorCommandBar />
        </div>
      </Show>
      <ProjectLauncher
        open={launcherOpen()}
        summaries={props.projects.summaries()}
        activeProjectId={props.shell.state.activeProjectId}
        onSwitch={(projectId) => openProject(projectId)}
        onManageAll={() => {
          setHomeSection("all");
          goHome();
        }}
        onNewProject={newProject}
        onClose={() => setLauncherOpen(false)}
      />
      <Crossbar
        open={crossbarOpen()}
        originProjectId={crossbarArgs()?.originProjectId ?? null}
        projectRoots={props.projects.byId(crossbarArgs()?.originProjectId ?? "")?.roots}
        originSessionId={
          props.appStore.state.currentSession?.project_id?.trim() ===
          (crossbarArgs()?.originProjectId ?? "").trim()
            ? props.appStore.state.currentSession?.id
            : undefined
        }
        seed={crossbarArgs()?.seed}
        seedSerial={crossbarArgs()?.serial}
        initialMode={crossbarArgs()?.mode}
        initialCommandId={crossbarArgs()?.commandId}
        gotoTargets={crossbarGotoTargets()}
        contributionCommands={crossbarActions()}
        onNavigateTarget={navigateFromCrossbarGoto}
        onOpenFolderAsProject={(path) => void proposeFolderAsProject(path)}
        onClose={() => closeCrossbar()}
        onEscalate={escalateCrossbarToSearch}
        onNavigateHit={(hit, matchText) => {
          openSearchHit(hit, (h) => {
            void navigateFromSearch(searchHitToNavTarget(h, matchText));
          });
        }}
      />
      <RecentChatSwitcher
        ref={props.recentSwitcherRef}
        entries={recentChatEntries()}
        onCommit={(entry) =>
          void navigateFromSearch({
            projectId: entry.projectId,
            sessionId: entry.sessionId,
          })
        }
      />
      <PeerViewSwitcher
        ref={props.peerViewSwitcherRef}
        views={focusedPeerViews()}
        onFocus={(view) => {
          void focusEditorWindow(view);
        }}
        onClose={(view) => {
          if (view.nativeLabel) void closeItemWindow(view.nativeLabel);
        }}
      />
      <ShortcutHelpOverlay
        open={helpOpen()}
        onClose={() => setHelpOpen(false)}
        onOpenKeyboardSettings={openKeyboardSettings}
      />
      <Show when={diffViewerRequest()} keyed>
        {(req) => {
          const project = req.projectId
            ? props.projects.byId(req.projectId)
            : undefined;
          const rootRefs = (project?.roots ?? []).map((root) => ({
            id: root.id,
            path: root.path,
            is_primary: root.is_primary,
          }));
          let request = req;
          const resolved = resolveSourceRequest({
            projectId: req.projectId ?? "",
            path: req.path,
            intent: "transient",
          });
          if (resolved.status === "resolved") {
            request = {
              ...req,
              absolutePath: resolved.request.absolutePath,
              rootId: resolved.request.rootId,
            };
          }
          return (
            <DiffViewerDrawer
              request={request}
              rootRefs={rootRefs}
            />
          );
        }}
      </Show>
      <FirstTimeTipsLayer />
  </>;
}
