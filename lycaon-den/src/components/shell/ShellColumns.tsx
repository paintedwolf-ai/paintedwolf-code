import { For, Show, onCleanup, type JSX } from "solid-js";
import type { ContextNavItemId } from "../../../shared/app-state-types.ts";
import type { OpenWorkerOptions } from "../../chat/worker/workers-model.ts";
import { tauriDragRegionProps } from "../../platform/runtime.ts";
import type { StageTabRail as StageTabRailState } from "../../shell/stage-tab-rail.ts";
import type { ActiveChat } from "../../shell/stage-scope.ts";
import {
  isRegionEntry,
  regionEntryTarget,
  registerFocusRegion,
  releaseFocusRegion,
} from "../../shortcuts/focus-region.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import type { Preparation } from "../../ui/presentation.ts";
import { parseChatSessionKey, type ResidentPresence, type ResidentStack } from "../../ui/resident-surfaces.ts";
import type { TabId } from "../chatview/tabs.ts";
import type { StageEdgePanes } from "../nav/StageEdgeControls.tsx";
import { StageTabRail } from "../nav/StageTabRail.tsx";
import { ShowLatest } from "../primitives/ShowLatest.tsx";
import { ResidentSurface } from "./ResidentSurface.tsx";
import { SessionDrawer, type SessionDrawerProps } from "./SessionDrawer.tsx";
import { ShellHeaderTitlebar } from "./ShellHeaderTitlebar.tsx";
import { StageBackChip, type StageBack } from "./StageBackChip.tsx";
import { StageErrorBoundary } from "./StageErrorBoundary.tsx";
import type { WindowEdgeClaim } from "./WindowControls.tsx";

/** Entering a stage lands in its editor, then the files tree, then its first control. */
const STAGE_ENTRY_PRIORITY = [
  ".cm-content",
  ".den-files-tree__label[tabindex='0']",
  ".den-files-tree__label",
  "input",
  "button",
  "[tabindex='0']",
] as const;

export function ShellStageColumn(props: {
  /** Retained surfaces keep the column mounted during handoff. */
  occupied: boolean;
  stageId: ContextNavItemId | null;
  stack: ResidentStack;
  headerLeadsWindow: boolean;
  back: StageBack | null;
  edges: StageEdgePanes;
  windowEdges: readonly WindowEdgeClaim[];
  appStore: AppStore;
  sessionTabs: StageTabRailState<TabId> & { showConversation: () => void };
  workers: {
    selectedWorkerId: () => string | null;
    openWorkers: (workerId?: string, opts?: OpenWorkerOptions) => void;
    worklogOpen: () => boolean;
    openWorklog: () => void;
  };
  notifications: JSX.Element | null;
  waitingVisible: boolean;
  preparation: Preparation | undefined;
  onReady: (key: string, generation?: number) => void;
  children: (surfaceKey: string) => JSX.Element;
}) {
  const focusClaim = {};
  onCleanup(() => releaseFocusRegion("context", focusClaim));
  const sessionTabs = {
    present: () => props.sessionTabs.sessionId() != null,
    children: () => (
      <ShowLatest when={props.sessionTabs.sessionId()} by={(sessionId) => sessionId}>
        {(sessionId) => (
          <StageTabRail
            appStore={props.appStore}
            sessionId={sessionId()}
            selectedWorkerId={props.workers.selectedWorkerId()}
            onOpenWorker={props.workers.openWorkers}
            worklogOpen={props.workers.worklogOpen()}
            onOpenWorklog={props.workers.openWorklog}
            openTab={props.sessionTabs.openTab()}
            onSelectTab={props.sessionTabs.selectTab}
            onShowConversation={props.sessionTabs.showConversation}
          />
        )}
      </ShowLatest>
    ),
  };
  return (
    <Show when={props.occupied || props.stack.keys().length > 0}>
      <div
        class="den-shell-stage--pane den-shell-stage"
        data-stage={props.stageId}
        tabindex="-1"
        ref={(el) => {
          if (el) {
            registerFocusRegion("context", el, focusClaim);
            const onFocus = () => {
              if (!isRegionEntry(el)) return;
              // Stage content, never the title-bar chrome above it.
              const main = el.querySelector<HTMLElement>(":scope > .den-shell-main");
              if (main) regionEntryTarget(main, STAGE_ENTRY_PRIORITY)?.focus();
            };
            el.addEventListener("focus", onFocus);
            onCleanup(() => el.removeEventListener("focus", onFocus));
          } else {
            releaseFocusRegion("context", focusClaim);
          }
        }}
      >
        <header
          class="den-shell-header"
          classList={{
            "den-shell-header--window-leading": props.headerLeadsWindow,
          }}
          {...chromeProps()}
          {...tauriDragRegionProps()}
        >
          {/* Title-bar restore controls preserve the stage position. */}
          <ShellHeaderTitlebar
            back={props.back}
            edges={props.edges}
            windowEdges={props.windowEdges}
            sessionTabs={sessionTabs}
          />
        </header>
        <main class="den-shell-main" aria-label="Stage">
          {props.notifications}
          <div class="den-resident-host">
            <For each={props.stack.keys()}>
              {(key) => (
                <ResidentSurface
                  surfaceKey={key}
                  presence={props.stack.presence(key)}
                  retained={
                    props.stack.pending() !== null &&
                    props.stack.presence(key) === "active"
                  }
                  onReady={props.onReady}
                  onWithdrawn={props.stack.withdraw}
                  waitingVisible={props.waitingVisible}
                  preparation={props.preparation}
                >
                  <StageErrorBoundary stage={`stage:${key}`}>
                    {props.children(key)}
                  </StageErrorBoundary>
                </ResidentSurface>
              )}
            </For>
          </div>
        </main>
      </div>
    </Show>
  );
}

export function ShellChatColumn(props: {
  stack: ResidentStack;
  sourceBack: StageBack | null;
  /** Keeps the active chat painted while its replacement prepares. */
  stageOpening: boolean;
  preparation: (projectId: string) => Preparation;
  children: (chat: ActiveChat, presence: () => ResidentPresence) => JSX.Element;
}) {
  return (
    <>
      <ShowLatest when={props.sourceBack}>{back => <div class="files-source-return"><StageBackChip back={back()} /></div>}</ShowLatest>
      <div class="den-resident-host">
        <For each={props.stack.keys()}>
          {(key) => {
            const chat = parseChatSessionKey(key);
            if (!chat) return null;
            return (
              <ResidentSurface
                surfaceKey={key}
                presence={props.stack.presence(key)}
                retained={
                  props.stack.presence(key) === "active" &&
                  (props.stack.pending() !== null || props.stageOpening)
                }
                onReady={props.stack.markReady}
                onWithdrawn={props.stack.withdraw}
                preparation={props.preparation(chat.projectId)}
              >
                {props.children(chat, () => props.stack.presence(key))}
              </ResidentSurface>
            );
          }}
        </For>
      </div>
    </>
  );
}

type SessionDrawerHostProps = Omit<SessionDrawerProps, "sessionId"> & {
  /** The session the drawer belongs to; none mounts nothing. */
  sessionId: string | undefined;
};

/** The session's drawer, kept mounted so worker transcripts stay warm while it is closed. */
export function ShellSessionDrawerHost(props: SessionDrawerHostProps) {
  return (
    <ShowLatest when={props.sessionId} by={(sessionId) => sessionId}>
      {(sessionId) => <SessionDrawer {...props} sessionId={sessionId()} />}
    </ShowLatest>
  );
}
