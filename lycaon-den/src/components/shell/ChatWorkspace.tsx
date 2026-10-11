import { type Nav } from "./shell-navigation-state.ts";
import { createShellOnboarding } from "./shell-onboarding.tsx";
import { createShellWorkerDrawer } from "./shell-worker-drawer.ts";
import { createShellPaneVisibility } from "./shell-pane-visibility.ts";
import { type ShellScope } from "./shell-scope.ts";
import { type LycaonClient } from "../../api/client.ts";
import { Show, createSignal, type JSX } from "solid-js";
import { ChatTabChromeProvider } from "../../chat/composer/chat-tab-chrome.tsx";
import { coordinatorModelVisionSupport } from "../../chat/composer/composer-vision.ts";
import { hasSpendRecoveryDraft } from "../../chat/recovery/spend-ceiling-recovery.ts";
import { isAnySessionActivityLive } from "../../chat/session/session-activity.ts";
import { sessionScope } from "../../notices/notice-scope.ts";
import { hasSharedNotices, selectSessionNotices } from "../../notices/notice-select.ts";
import { type NoticeStore } from "../../notices/notice-store.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { tauriDragRegionProps } from "../../platform/runtime.ts";
import { createVerifyTestSuggestion } from "../../settings/extensions/verify-test-suggestion.ts";
import { type ActiveChat } from "../../shell/stage-scope.ts";
import { type CostStore } from "../../store/cost-store.ts";
import { type SettingsStore } from "../../store/settings-store.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { ChatView } from "../chatview/ChatView.tsx";
import { DraftPromoteBanner } from "../home/DraftPromoteBanner.tsx";
import { NoFolderBanner } from "../home/NoFolderBanner.tsx";
import { ChatTabRail } from "../nav/ChatTabRail.tsx";
import { ChatTopChromeStack } from "../nav/ChatTopChromeStack.tsx";
import { NoProviderCard } from "../NoProviderCard.tsx";
import { NoticeRail } from "../NoticeRail.tsx";
import { SpendCeilingApproachingNudge, spendCeilingApproachingVisible } from "../SpendCeilingApproachingNudge.tsx";
import { SpendCeilingReachedNudge, spendCeilingReachedNotice } from "../SpendCeilingReachedNudge.tsx";
import { VerifyTestNudge } from "../VerifyTestNudge.tsx";
import { ChromeDragSurface } from "./ChromeDragSurface.tsx";
import { ContextDrawerHost } from "./ContextDrawer.tsx";
import { SessionProtectionBanners, sessionProtectionPresent, type ProtectionBannerId } from "./SessionProtectionBanners.tsx";
import { StageErrorBoundary } from "./StageErrorBoundary.tsx";
import { type ShellChatActions } from "./shell-chat-actions.ts";

type Props = Pick<ShellScope, "shell" | "appStore" | "recents" | "projects"> & {
  chat: ActiveChat;
  pending: () => boolean;
  surfaceActive: () => boolean;
  settingsStore: SettingsStore;
  costStore: CostStore;
  notices: NoticeStore;
  workerDrawer: Pick<ReturnType<typeof createShellWorkerDrawer>, "selectedWorkerId" | "workersOpen" | "workerDrawerFocus" | "closeWorkers" | "acknowledgeDrawerFocus" | "openWorkers">;
  panes: Pick<ReturnType<typeof createShellPaneVisibility>, "chatHeaderLeadsWindow" | "windowNavCollapsed" | "navTouchesChat" | "navAutomaticallyCollapsed" | "contextHidden" | "navSide" | "showWindowNav" | "conversationSeam">;
  actions: ShellChatActions;
  onboarding: Pick<ReturnType<typeof createShellOnboarding>, "showNoProviderBanner" | "providerGap">;
  projectDirForChat: () => string;
  nav: () => Nav;
  splitLive: () => boolean;
  notificationStack: () => JSX.Element;
  showPromoteNudge: () => boolean;
  showNoFolderBanner: () => boolean;
  onPromote: (projectId: string) => void;
  dismissDraftPromoteBanner: (projectId: string) => void;
  dismissNoFolderBanner: (projectId: string) => void;
  addFolderToActiveProject: () => Promise<unknown>;
  onOpenBudgets: () => void;
  onOpenProviders: () => void;
  onOpenTests: () => void;
  onOpenFiles: () => void;
};

export function ChatWorkspace(props: Props) {
  const layer = props;
  const { projectDirForChat, nav, splitLive, notificationStack, showPromoteNudge,
    showNoFolderBanner, dismissDraftPromoteBanner, dismissNoFolderBanner, addFolderToActiveProject } = props;
  const { selectedWorkerId, workersOpen, workerDrawerFocus, closeWorkers,
    acknowledgeDrawerFocus, openWorkers } = props.workerDrawer;
  const { chatHeaderLeadsWindow, windowNavCollapsed, navTouchesChat, navAutomaticallyCollapsed,
    contextHidden, navSide, showWindowNav, conversationSeam } = props.panes;
  const { sendPrompt, stopChat, resumeSpendLimitedChat } = props.actions;
  const { showNoProviderBanner, providerGap } = props.onboarding;
  const noticeIndex = () => props.notices.index();
  const connectedClient = () => props.appStore.state.sidecarStatus === "connected" ? getLycaonClient() : null;
  const openBudgetsSettings = props.onOpenBudgets;
  const openProvidersSettings = props.onOpenProviders;
  const openMoveDialog = props.onPromote;
  const chatTopDock = (chat: ActiveChat) => {
    const [dismissedProtection, setDismissedProtection] = createSignal<
      Partial<Record<ProtectionBannerId, boolean>>
    >({});
    const [providerDismissed, setProviderDismissed] = createSignal(false);
    const protection = () => {
      const sess = props.appStore.state.currentSession;
      if (!sess || sess.id !== chat.sessionId) return undefined;
      return sess.ui?.protection;
    };
    const spendLimits = () =>
      props.settingsStore.state.effectiveLimits?.projectId === chat.projectId
        ? props.settingsStore.state.effectiveLimits.limits
        : props.settingsStore.state.limits ?? null;
    const updateSpendLimits = (client: LycaonClient, update: import("../../settings/budgets/spend-ceiling-actions.ts").SpendCeilingUpdate) => {
      if (update.scope === "project") {
        props.settingsStore.actions.setEffectiveLimits(chat.projectId, update.limits);
        return;
      }
      props.settingsStore.actions.setLimits(update.limits);
      void client.getLimitsSettings(chat.projectId).then((limits) => {
        props.settingsStore.actions.setEffectiveLimits(chat.projectId, limits);
      });
    };
    const verify = createVerifyTestSuggestion({
      client: connectedClient,
      appStore: props.appStore,
      projectId: () => chat.projectId,
    });
    return (
      <ChatTopChromeStack
        session={{
          present: () =>
            selectSessionNotices(noticeIndex(), chat.sessionId).length > 0,
          children: () => (
            <NoticeRail
              notices={selectSessionNotices(noticeIndex(), chat.sessionId)}
              onDismiss={(id) => props.notices.dismiss(id)}
              onDismissAll={() =>
                props.notices.dismissScope(
                  sessionScope(chat.projectId, chat.sessionId),
                )
              }
              announceLive={!isAnySessionActivityLive(props.appStore)}
            />
          ),
        }}
        spend={{
          present: () =>
            spendCeilingReachedNotice(props.notices, chat.sessionId) != null ||
            spendCeilingApproachingVisible({
              sessionId: chat.sessionId,
              limits: spendLimits(),
              summary: props.costStore.state.session,
            }),
          children: () => (
            <Show when={connectedClient()} keyed>
              {(client) => (
                <>
                  <SpendCeilingReachedNudge
                    client={client}
                    notices={props.notices}
                    sessionId={chat.sessionId}
                    projectId={chat.projectId}
                    spentUsd={
                      props.costStore.state.session?.estimated_nano_usd != null
                        ? props.costStore.state.session.estimated_nano_usd / 1e9
                        : undefined
                    }
                    hasUnsentContent={hasSpendRecoveryDraft(chat.sessionId)}
                    onOpenBudgets={openBudgetsSettings}
                    onResume={() => resumeSpendLimitedChat(chat)}
                    onLimitsUpdated={(update) => updateSpendLimits(client, update)}
                  />
                  <SpendCeilingApproachingNudge
                    client={client}
                    sessionId={chat.sessionId}
                    projectId={chat.projectId}
                    summary={props.costStore.state.session}
                    limits={spendLimits()}
                    onLimitsUpdated={(update) => updateSpendLimits(client, update)}
                    onOpenBudgets={openBudgetsSettings}
                  />
                </>
              )}
            </Show>
          ),
        }}
        notifications={{
          present: () => hasSharedNotices(noticeIndex()),
          children: notificationStack,
        }}
        verify={{
          present: () => verify.suggesting(),
          children: () => (
            <VerifyTestNudge
              suggestion={verify}
              onOpenSettings={() =>
                props.onOpenTests()
              }
            />
          ),
        }}
        protection={{
          present: () =>
            sessionProtectionPresent(protection(), dismissedProtection()),
          children: () => (
            <SessionProtectionBanners
              protection={protection()}
              onDismiss={(id) =>
                setDismissedProtection((prev) => ({ ...prev, [id]: true }))
              }
            />
          ),
        }}
        promote={{
          present: showPromoteNudge,
          children: () => (
            <DraftPromoteBanner
              onPromote={() => {
                const id = props.shell.state.activeProjectId;
                if (id) openMoveDialog(id);
              }}
              onDismiss={() => {
                const projectId = props.shell.state.activeProjectId;
                if (projectId) dismissDraftPromoteBanner(projectId);
              }}
            />
          ),
        }}
        provider={{
          present: () => showNoProviderBanner() && !providerDismissed(),
          children: () => (
            <Show when={providerGap()} keyed>
              {(gap) => (
                <NoProviderCard
                  gap={gap}
                  onOpenProviders={openProvidersSettings}
                  onDismiss={() => setProviderDismissed(true)}
                />
              )}
            </Show>
          ),
        }}
        folder={{
          present: showNoFolderBanner,
          children: () => (
            <NoFolderBanner
              onAddFolder={() => void addFolderToActiveProject()}
              onDismiss={() => {
                const projectId = props.shell.state.activeProjectId;
                if (projectId) dismissNoFolderBanner(projectId);
              }}
            />
          ),
        }}
      />
    );
  };

  const ChatStageBody = (body: {
    chat: ActiveChat;
    surfaceActive: () => boolean;
  }) => (
    <>
      <ChatView
        needsProvider={showNoProviderBanner()}
        appStore={props.appStore}
        recents={props.recents}
        projects={props.projects}
        projectId={body.chat.projectId}
        projectDir={projectDirForChat()}
        sessionId={body.chat.sessionId}
        hasInitialPrompt={
          props.shell.state.foreground?.sessionId === body.chat.sessionId &&
          props.shell.state.foreground.initialPrompt === true
        }
        surfaceActive={body.surfaceActive()}
        selectedWorkerId={selectedWorkerId()}
        workersDrawerOpen={body.surfaceActive() && workersOpen()}
        workerDrawerFocus={workerDrawerFocus()}
        workersBackgroundHydrate={nav() === "projects"}
        onWorkersClose={closeWorkers}
        onWorkerDrawerFocusHandled={acknowledgeDrawerFocus}
        onOpenWorker={openWorkers}
        onOpenFiles={props.onOpenFiles}
        onSend={sendPrompt}
        onStop={stopChat}
        visionSupport={coordinatorModelVisionSupport(
          props.settingsStore.state.providers,
          props.settingsStore.state.modelPolicy,
        )}
        providers={props.settingsStore.state.providers}
        providerKinds={props.settingsStore.state.providerKinds}
      />
    </>
  );

    const dock = chatTopDock(layer.chat);
    return (
    <ChatTabChromeProvider sessionKey={layer.chat.sessionId}>
      <ContextDrawerHost>
        <div
          class="den-shell-stage--chat den-shell-stage"
          classList={{
            "den-stage-boot": layer.pending() === true,
            "den-stage-enter-fade": layer.pending() === true,
          }}
          data-boot={layer.pending() ? "ready" : undefined}
        >
          <header
            class="den-shell-header-chat den-shell-header"
            classList={{
              "den-shell-header--window-leading": chatHeaderLeadsWindow(),
            }}
            {...chromeProps()}
            {...tauriDragRegionProps()}
          >
            <ChromeDragSurface />
            <ChatTabRail
              appStore={props.appStore}
              sessionId={layer.chat.sessionId}
              selectedWorkerId={selectedWorkerId()}
              onOpenWorker={openWorkers}
              navCollapsed={
                windowNavCollapsed() && (navTouchesChat() || splitLive())
              }
              navAutomaticallyCollapsed={navAutomaticallyCollapsed()}
              navSplitFallback={!navTouchesChat() && !contextHidden()}
              navSide={navSide()}
              onNavExpand={() => void showWindowNav()}
              conversationSeam={conversationSeam()}
              dock={dock}
            />
          </header>
          <main class="den-shell-main" aria-label="Chat">
            <StageErrorBoundary stage="chat">
              <ChatStageBody
                chat={layer.chat}
                surfaceActive={layer.surfaceActive}
              />
            </StageErrorBoundary>
          </main>
        </div>
      </ContextDrawerHost>
    </ChatTabChromeProvider>
    );
  }
