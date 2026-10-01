import { For, Show, onCleanup, type JSX } from "solid-js";
import type { ContextNavItemId } from "../../../shared/app-state-types.ts";
import { tauriDragRegionProps } from "../../platform/runtime.ts";
import type { ActiveChat } from "../../shell/stage-scope.ts";
import {
  isRegionEntry,
  regionEntryTarget,
  registerFocusRegion,
  releaseFocusRegion,
} from "../../shortcuts/focus-region.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import type { Preparation } from "../../ui/presentation.ts";
import { parseChatSessionKey, type ResidentPresence, type ResidentStack } from "../../ui/resident-surfaces.ts";
import type { StageEdgePanes } from "../nav/StageEdgeControls.tsx";
import { ShowLatest } from "../primitives/ShowLatest.tsx";
import { ResidentSurface } from "./ResidentSurface.tsx";
import { ShellHeaderTitlebar } from "./ShellHeaderTitlebar.tsx";
import { StageBackChip, type StageBack } from "./StageBackChip.tsx";
import { StageErrorBoundary } from "./StageErrorBoundary.tsx";

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
  notifications: JSX.Element | null;
  waitingVisible: boolean;
  preparation: Preparation | undefined;
  onReady: (key: string, generation?: number) => void;
  children: (surfaceKey: string) => JSX.Element;
}) {
  const focusClaim = {};
  onCleanup(() => releaseFocusRegion("context", focusClaim));
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
          <ShellHeaderTitlebar back={props.back} edges={props.edges} />
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
