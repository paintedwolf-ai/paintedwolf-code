import { ShowLatest } from "../primitives/ShowLatest.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import type { NoticeIndex } from "../../notices/notice-store.ts";
import { Show, type JSX } from "solid-js";
import type { ProjectRoot } from "../../api/types.ts";
import type { AttentionIndex } from "../../attention/attention-model.ts";
import type { ChatListSort, ContextNavItemId } from "../../../shared/app-state-types.ts";
import type { ProjectSummary } from "../../project/project-summary.ts";
import { PROJECT_SETTINGS_OVERLAY_COPY } from "../../settings/security/project-settings-overlay-copy.ts";
import type {
  SidebarChatRow,
  SidebarChatSections,
} from "../../store/projects-sidebar-model.ts";
import type { ItemWindowView } from "../../platform/windows/item-windows.ts";
import { FocusedSessionList } from "./FocusedSessionList.tsx";
import { ProjectContextZone } from "./ProjectContextZone.tsx";
import { ProjectFolderZone } from "./ProjectFolderZone.tsx";
import { ProjectSwitcherButton } from "./ProjectSwitcherButton.tsx";

type ActiveChat = { projectId: string; sessionId: string | null };

type Props = {
  activeProject: ProjectSummary | null;
  roots: ProjectRoot[];
  /** Pinned chats and the first chats in the chosen order (host inventory). */
  sessions: SidebarChatSections;
  chatSort: ChatListSort;
  /** The listed chats already follow `chatSort`. */
  chatSortSettled: boolean;
  onChatSortChange: (sort: ChatListSort) => void;
  sessionsLoaded: boolean;
  sessionsError?: string;
  onRetrySessions?: () => void;
  /** Total active chats in the project — the All chats affordance count. */
  totalChats: number;
  attention?: AttentionIndex;
  /** Notice counts keyed by chat scope. */
  noticeIndex?: () => NoticeIndex;
  /** The project record is ready for mounting behind the opening cover. */
  identityLoaded: boolean;
  activeChat: ActiveChat | null;
  newChatActive: boolean;
  onNewChat: (projectId: string) => void;
  onSelectSession: (row: SidebarChatRow) => void;
  onRenameSession: (row: SidebarChatRow, title: string) => void | Promise<void>;
  onTogglePin: (row: SidebarChatRow, pinned: boolean) => void;
  onMovePin: (row: SidebarChatRow, position: number) => void;
  onArchiveSession: (row: SidebarChatRow) => void;
  onDeleteSession: (row: SidebarChatRow) => void;
  sessionWindowIds?: ReadonlySet<string>;
  sessionWindowCounts?: ReadonlyMap<string, number>;
  sessionWindowViewNumbers?: ReadonlyMap<string, readonly number[]>;
  onOpenSessionInNewWindow?: (row: SidebarChatRow) => void;
  onRaiseSessionWindow?: (row: SidebarChatRow, viewNumber?: number) => void;
  onCloseSessionWindow?: (row: SidebarChatRow, viewNumber: number) => void;
  onOpenAllChats: () => void;
  allChatsActive: boolean;
  onOpenLauncher: () => void;
  onAddFolder: () => void;
  onDetachFolder: (rootId: string) => void;
  onOpenStage: (stageId: ContextNavItemId) => void;
  isStageActive: (stageId: ContextNavItemId) => boolean;
  isStageAvailable: (stageId: ContextNavItemId) => boolean;
  /** A split is on screen — Context entries open into its stage column. */
  splitLive?: boolean;
  detachedStages?: ReadonlySet<ContextNavItemId>;
  contextWindowViewNumbers?: ReadonlyMap<ContextNavItemId, readonly number[]>;
  projectViews?: readonly ItemWindowView[];
  onOpenInNewWindow?: (stageId: ContextNavItemId) => void;
  onRaiseWindow?: (stageId: ContextNavItemId, viewNumber?: number) => void;
  onCloseContextWindow?: (stageId: ContextNavItemId, viewNumber: number) => void;
  onFocusProjectView?: (label: string) => void;
  onCloseProjectView?: (label: string) => void;
  onOpenConfiguration: () => void;
  configActive: boolean;
  isDraft?: boolean;
  promotePending?: boolean;
  promotionPhase?: string;
  promotionError?: string;
  onSaveDraft?: () => void;
  onRetryPromotion?: () => void;
  onCancelPromotion?: () => void;
  /** Marks the selected chat while it is visible in either layout. */
  chatFocused: boolean;
  /** Status controls below the project identity. */
  statusChips?: JSX.Element;
  /** False keeps the mounted chips invisible until the workspace paints. */
  statusRevealed?: boolean;
};

function ClusterIconButton(props: {
  testId: string;
  label: string;
  tip: string;
  active?: boolean;
  onClick: () => void;
  children: JSX.Element;
}) {
  return (
    <button
      type="button"
      class="project-cluster__icon-btn den-inset-icon-btn"
      classList={{ "project-cluster__icon-btn--active": props.active === true }}
      data-testid={props.testId}
      aria-label={props.label}
      data-tip={props.tip}
      aria-pressed={props.active === true ? true : undefined}
      onClick={() => props.onClick()}
    >
      {props.children}
    </button>
  );
}

function GearIcon() {
  return (
    <ThemeIcon slot="settings" size={16} />
  );
}

export function FocusedProjectNav(props: Props) {
  const hasFolder = () => props.roots.length > 0;

  return (
    <div class="focused-project-nav" data-testid="focused-project-nav">
      <Show when={props.identityLoaded} fallback={<p class="focused-project-nav__empty">Loading…</p>}>
        {/* Project identity keeps the rail mounted across summary refreshes. */}
        <Show
          when={props.activeProject?.id}
          keyed
          fallback={
            <div class="focused-project-nav__missing" data-testid="focused-project-nav-no-project">
              <p class="focused-project-nav__empty">
                No active project. Open the launcher or start a new chat.
              </p>
            </div>
          }
        >
          {(projectId) => (
            <>
              <div class="project-cluster" data-testid="project-cluster">
                <div class="project-cluster__header">
                  <ShowLatest when={props.activeProject}>
                    {(project) => (
                      <ProjectSwitcherButton
                        project={project()}
                        onOpenLauncher={() => props.onOpenLauncher()}
                      />
                    )}
                  </ShowLatest>
                  <ClusterIconButton
                    testId="project-configuration-entry"
                    label={PROJECT_SETTINGS_OVERLAY_COPY.stageTitle}
                    tip={PROJECT_SETTINGS_OVERLAY_COPY.stageTitle}
                    active={props.configActive}
                    onClick={() => props.onOpenConfiguration()}
                  >
                    <GearIcon />
                  </ClusterIconButton>
                </div>
                <div class="project-cluster__rule" aria-hidden="true" />
                <ProjectFolderZone
                  roots={props.roots}
                  projectId={projectId}
                  isDraft={props.isDraft}
                  promotePending={props.promotePending}
                  promotionPhase={props.promotionPhase}
                  promotionError={props.promotionError}
                  onSaveDraft={() => props.onSaveDraft?.()}
                  onRetryPromotion={() => props.onRetryPromotion?.()}
                  onCancelPromotion={() => props.onCancelPromotion?.()}
                  onAddFolder={() => props.onAddFolder()}
                  onDetach={(rootId) => props.onDetachFolder(rootId)}
                />
              </div>
              {/* Reserves space while project status prepares. */}
              <div
                class="focused-project-nav__status"
                data-presentation={props.statusRevealed === false ? "preparing" : "published"}
              >
                {props.statusChips}
              </div>
              <button
                type="button"
                class="new-chat-btn"
                classList={{
                  "new-chat-btn--muted": !hasFolder(),
                  "new-chat-btn--current": props.newChatActive,
                }}
                data-testid="new-chat-btn"
                disabled={props.newChatActive}
                aria-current={props.newChatActive ? "page" : undefined}
                onClick={() => props.onNewChat(projectId)}
              >
                <ThemeIcon slot="new-session" size={15} />
                New chat
              </button>
              <Show when={props.sessionsLoaded && props.sessionsError}>
                <div class="focused-project-nav__empty" role="alert" data-testid="project-chats-load-error">
                  <p>{props.sessionsError}</p>
                  <DenButton variant="link" compact onClick={() => props.onRetrySessions?.()}>Retry</DenButton>
                </div>
              </Show>
              <FocusedSessionList
                projectId={projectId}
                sections={props.sessions}
                sort={props.chatSort}
                sortSettled={props.chatSortSettled}
                onSortChange={(sort) => props.onChatSortChange(sort)}
                loaded={props.sessionsLoaded}
                totalChats={props.totalChats}
                attention={props.attention}
                noticeIndex={props.noticeIndex}
                activeChat={props.activeChat}
                focused={props.chatFocused}
                allChatsActive={props.allChatsActive}
                onSelectSession={(row) => props.onSelectSession(row)}
                onRenameSession={(row, title) => props.onRenameSession(row, title)}
                onTogglePin={(row, pinned) => props.onTogglePin(row, pinned)}
                onMovePin={(row, position) => props.onMovePin(row, position)}
                onArchiveSession={(row) => props.onArchiveSession(row)}
                onDeleteSession={(row) => props.onDeleteSession(row)}
                sessionWindowIds={props.sessionWindowIds}
                sessionWindowCounts={props.sessionWindowCounts}
                sessionWindowViewNumbers={props.sessionWindowViewNumbers}
                onOpenInNewWindow={props.onOpenSessionInNewWindow}
                onRaiseWindow={props.onRaiseSessionWindow}
                onCloseWindow={props.onCloseSessionWindow}
                onOpenAllChats={() => props.onOpenAllChats()}
              />
              <ProjectContextZone
                projectId={projectId}
                onOpenStage={props.onOpenStage}
                isStageActive={props.isStageActive}
                isStageAvailable={props.isStageAvailable}
                splitLive={props.splitLive}
                detachedStages={props.detachedStages}
                windowViewNumbers={props.contextWindowViewNumbers}
                projectViews={props.projectViews}
                onOpenInNewWindow={props.onOpenInNewWindow}
                onRaiseWindow={props.onRaiseWindow}
                onCloseWindow={props.onCloseContextWindow}
                onFocusProjectView={props.onFocusProjectView}
                onCloseProjectView={props.onCloseProjectView}
              />
            </>
          )}
        </Show>
      </Show>
    </div>
  );
}
