import { Show } from "solid-js";
import type {
  ContextNavItemId,
  StagePlacement,
} from "../../../shared/app-state-types.ts";
import { ariaKeyShortcutsForHandler, bindingForHandler } from "../../shortcuts/display-binding-for.ts";
import { isTauriRuntime } from "../../platform/runtime.ts";
import { workspaceOrientationPref } from "../../shell/layout-store.ts";
import { stageLabelFor } from "../stage/stage-registry.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import type { IconSlot } from "../../contributions/theme-vocabulary.generated.ts";

export type LayoutDockButtonProps = {
  open: boolean;
  splitLive: boolean;
  /** Null on Home — workspace-wide controls remain available. */
  stageId: ContextNavItemId | null;
  onToggle: () => void;
  elementRef?: (el: HTMLButtonElement) => void;
};

export function LayoutDockButton(props: LayoutDockButtonProps) {
  return (
    <button
      ref={props.elementRef}
      type="button"
      class="den-shell-dock-link"
      classList={{ "den-shell-dock-link-active": props.open }}
      data-testid="layout-dock-btn"
      aria-haspopup="dialog"
      aria-expanded={props.open}
      aria-label={
        props.stageId
          ? `Layout · ${stageLabelFor(props.stageId)}`
          : "Layout"
      }
      onClick={() => props.onToggle()}
    >
      <span
        class="den-shell-dock-link__icon den-layout-glyph"
        classList={{
          "den-layout-glyph--split": props.splitLive,
          "den-layout-glyph--inline": !props.splitLive,
        }}
        aria-hidden="true"
      >
        <i />
      </span>
      <span class="den-shell-dock-link__label">Layout</span>
    </button>
  );
}

export type LayoutDockTrayProps = {
  /** Null on Home, where workspace-wide settings remain available. */
  stageId: ContextNavItemId | null;
  placement: StagePlacement;
  splitLive: boolean;
  /** True when this placement would widen the native window. */
  widensFor?: (placement: StagePlacement) => boolean;
  hasConversation: boolean;
  onPlacement?: (placement: StagePlacement) => void;
  /** One or more peer windows show this stage. */
  stageInWindow: boolean;
  /** Opens a peer window, or closes this stage's peer windows. */
  onStageWindowChange?: (inWindow: boolean) => void;
  onToggleOrientation: () => void;
  onSwapColumns: () => void;
  onResetSplitSize: () => void;
  /** The live split's conversation column is hidden. */
  conversationHidden: boolean;
  contextHidden: boolean;
  onToggleContext: () => void;
  onToggleConversation: () => void;
};

export function LayoutDockTray(props: LayoutDockTrayProps) {
  const stageLabel = () =>
    props.stageId ? stageLabelFor(props.stageId) : "";
  // Only native windows can offer a widening hint.
  const placementHint = (kind: StagePlacement, label: string) =>
    isTauriRuntime() && props.widensFor?.(kind) === true
      ? `${label} — widens the window to make room.`
      : undefined;
  const showWindowAction = () =>
    props.stageId != null &&
    isTauriRuntime() &&
    props.onStageWindowChange != null;

  return (
    <div
      class="den-layout-tray"
      role="dialog"
      aria-label={
        props.stageId ? `Layout · ${stageLabel().toLocaleLowerCase()}` : "Layout"
      }
      data-testid="layout-tray"
      {...chromeProps()}
    >
      <Show when={props.stageId} keyed>
        {(stageId) => (
          <div class="den-layout-tray__section">
            <div class="den-layout-tray__heading">View</div>
            <div class="den-layout-tray__tiles">
              <PlacementTile
                kind="inline"
                label="Single view"
                hint={placementHint("inline", "Single view")}
                active={props.placement === "inline"}
                onSelect={() => props.onPlacement?.("inline")}
              />
              <PlacementTile
                kind="split"
                label="Split view"
                hint={placementHint("split", "Split view")}
                active={props.placement === "split"}
                onSelect={() => props.onPlacement?.("split")}
              />
            </div>
            <Show
              when={props.hasConversation}
              fallback={
                <p class="den-layout-tray__description">
                  Split view opens a conversation beside{" "}
                  <b>{stageLabelFor(stageId).toLocaleLowerCase()}</b>.
                </p>
              }
            >
              <p class="den-layout-tray__description">
                Show <b>{stageLabelFor(stageId).toLocaleLowerCase()}</b> beside
                this conversation.
              </p>
              <p
                class="den-layout-tray__description"
                data-testid="layout-tray-collapse-note"
              >
                Hide either pane when you need the room; restore it without losing your place.
              </p>
            </Show>
          </div>
        )}
      </Show>

      <div class="den-layout-tray__section">
        <div class="den-layout-tray__actions">
          <Show when={showWindowAction()}>
            <TrayAction
              testId="layout-stage-window"
              icon="new-window"
              label={
                props.stageInWindow
                  ? `Close ${stageLabel()} window`
                  : `Open ${stageLabel()} in new window`
              }
              onSelect={() => props.onStageWindowChange?.(!props.stageInWindow)}
            />
          </Show>
          <TrayAction
            testId="layout-toggle-conversation"
            label={props.conversationHidden ? "Show conversation" : "Hide conversation"}
            handlerId="layout.toggleConversation"
            disabled={!props.splitLive}
            onSelect={() => props.onToggleConversation()}
          />
          <TrayAction
            testId="layout-toggle-context"
            label={props.contextHidden ? "Show context" : "Hide context"}
            handlerId="layout.toggleContext"
            disabled={!props.splitLive}
            onSelect={() => props.onToggleContext()}
          />
          <TrayAction
            testId="layout-swap-columns"
            label="Swap chat and context"
            handlerId="layout.swapColumns"
            disabled={!props.splitLive}
            onSelect={() => props.onSwapColumns()}
          />
          <TrayAction
            testId="layout-toggle-orientation"
            label="Mirror workspace"
            handlerId="layout.toggleOrientation"
            pressed={workspaceOrientationPref() === "mirrored"}
            onSelect={() => props.onToggleOrientation()}
          />
          <TrayAction
            testId="layout-reset-split-size"
            label="Reset size"
            handlerId="layout.resetSplitSize"
            disabled={!props.splitLive}
            onSelect={() => props.onResetSplitSize()}
          />
        </div>
        <p class="den-layout-tray__foot">Layout changes apply to this window.</p>
      </div>
    </div>
  );
}

function TrayAction(props: {
  testId: string;
  label: string;
  icon?: IconSlot;
  /** Omitted when the action has no binding. */
  handlerId?: string;
  /** Set only on toggles; the button then shows its state. */
  pressed?: boolean;
  disabled?: boolean;
  onSelect: () => void;
}) {
  const binding = () => {
    if (!props.handlerId) return undefined;
    const value = bindingForHandler(props.handlerId);
    return value === "—" ? undefined : value;
  };
  return (
    <button
      type="button"
      class="den-layout-tray__action"
      classList={{ "den-layout-tray__action--active": props.pressed === true }}
      data-testid={props.testId}
      aria-label={props.label}
      aria-keyshortcuts={props.handlerId ? ariaKeyShortcutsForHandler(props.handlerId) : undefined}
      aria-pressed={props.pressed}
      disabled={props.disabled}
      onClick={() => props.onSelect()}
    >
      <Show when={props.icon} keyed>
        {(slot) => <ThemeIcon slot={slot} size={14} />}
      </Show>
      <span>{props.label}</span>
      <Show when={binding()} keyed>
        {(value) => <kbd>{value}</kbd>}
      </Show>
    </button>
  );
}

function PlacementTile(props: {
  kind: StagePlacement;
  label: string;
  /** Hover tip when the press also widens the window. */
  hint?: string;
  active: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      class="den-layout-tray__tile"
      classList={{
        "den-layout-tray__tile--active": props.active,
      }}
      aria-pressed={props.active}
      data-tip={props.hint}
      data-testid={`layout-tile-${props.kind}`}
      onClick={() => props.onSelect()}
    >
      <span
        class={`den-layout-glyph den-layout-glyph--${props.kind}`}
        aria-hidden="true"
      >
        <i />
      </span>
      <span class="den-layout-tray__tile-label">{props.label}</span>
    </button>
  );
}
