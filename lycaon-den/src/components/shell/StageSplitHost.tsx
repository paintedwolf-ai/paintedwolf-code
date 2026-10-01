import { Show, type JSX } from "solid-js";
import type { NarrowSplitSurvivor } from "../../../shared/app-state-types.ts";
import { LAYOUT_BAND_SCALES, bindLayoutBand } from "../../layout/layout-bands.ts";

export type StageSplitHostProps = {
  splitLive: boolean;
  /** Whether each column currently holds content; an empty one collapses. */
  stageColumnOccupied: boolean;
  chatColumnOccupied: boolean;
  stageOnLeft: boolean;
  /**
   * The conversation column gives the stage the whole split. `preview` follows a
   * divider drag and keeps the divider under the pointer; `hidden` is committed.
   */
  contextHidden?: boolean;
  conversationHidden?: "preview" | "hidden" | null;
  /**
   * Column the stylesheet keeps once the host band reports too little room for
   * both. Declared from preferences; the width that triggers it is measured.
   */
  narrowSurvivor: NarrowSplitSurvivor;
  /** Effective conversation column width, including drag preview. */
  chatWidthPx: number;
  washed: boolean;
  stageColDual: boolean;
  chatColDual: boolean;
  hostRef?: (el: HTMLDivElement) => void;
  /** Marks stage focus on pointer or focus entry. */
  onStageEnter: () => void;
  /** Marks chat focus on pointer or focus entry. */
  onChatEnter: () => void;
  stageColumn: JSX.Element;
  chatColumn: JSX.Element;
  divider: JSX.Element;
};

/** Split host with stable stage and chat columns. */
export function StageSplitHost(props: StageSplitHostProps) {
  const stageCol = () => (
    <div
      ref={(el) => bindLayoutBand(el, LAYOUT_BAND_SCALES.stageColumn)}
      class="den-split-col den-split-col--stage"
      classList={{ "den-split-col--dual": props.stageColDual }}
      data-testid="split-col-stage"
      aria-label="Stage"
      aria-hidden={props.splitLive && props.contextHidden ? true : undefined}
      inert={props.splitLive && props.contextHidden ? true : undefined}
      onFocusIn={props.onStageEnter}
      on:pointerdown={{ handleEvent: () => props.onStageEnter(), capture: true }}
    >
      {props.stageColumn}
    </div>
  );

  const conversationHidden = () =>
    props.splitLive ? (props.conversationHidden ?? null) : null;
  // Mounted but out of reach, like the collapsed sidebar.
  const chatColOut = () => conversationHidden() === "hidden";

  const chatCol = () => (
    <div
      ref={(el) => bindLayoutBand(el, LAYOUT_BAND_SCALES.chatColumn)}
      class="den-split-col den-split-col--chat"
      classList={{ "den-split-col--dual": props.chatColDual }}
      data-testid="split-col-chat"
      aria-label="Conversation"
      aria-hidden={chatColOut() ? true : undefined}
      inert={chatColOut() ? true : undefined}
      onFocusIn={props.onChatEnter}
      on:pointerdown={{ handleEvent: () => props.onChatEnter(), capture: true }}
    >
      {props.chatColumn}
    </div>
  );

  return (
    <div
      ref={(el) => {
        bindLayoutBand(el, LAYOUT_BAND_SCALES.stageHost);
        props.hostRef?.(el);
      }}
      class="den-shell-stage-host"
      classList={{
        "den-shell-stage-host--washed": props.washed,
        "den-shell-stage-host--split": props.splitLive,
        "den-shell-stage-host--handoff":
          !props.splitLive &&
          props.stageColumnOccupied &&
          props.chatColumnOccupied,
      }}
      data-stage-leading={props.splitLive ? String(props.stageOnLeft) : undefined}
      data-context-hidden={props.splitLive && props.contextHidden ? "hidden" : undefined}
      data-conversation-hidden={conversationHidden() ?? undefined}
      data-narrow-survivor={props.splitLive ? props.narrowSurvivor : undefined}
      style={
        props.splitLive
          ? {
              "--den-split-chat-w": `${props.chatWidthPx}px`,
            }
          : undefined
      }
      data-stage-occupied={String(props.stageColumnOccupied)}
      data-chat-occupied={String(props.chatColumnOccupied)}
      data-testid="shell-stage-host"
    >
      <div class="den-shell-stage-wash den-stage-wash-fill" aria-hidden="true" />
      {/* Mounted columns preserve view state. */}
      {stageCol()}
      <Show when={props.splitLive}>{props.divider}</Show>
      {chatCol()}
    </div>
  );
}
