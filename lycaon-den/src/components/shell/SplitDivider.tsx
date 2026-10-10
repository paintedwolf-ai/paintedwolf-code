import { createEffect, on, onCleanup } from "solid-js";
import { startPointerResize } from "../../layout/pointer-resize.ts";
import type { ResizeSession } from "../../layout/resize-session.ts";
import { dragRequestsHide, PANE_HIDDEN_PX } from "../../layout/drag-to-hide.ts";
import {
  CHAT_COL_MIN,
  clampChatWidthPx,
  DIVIDER_PX,
  maxChatWidthPx,
  snapChatWidthPx,
  stageShare,
} from "../../shell/stage-placement.ts";

export type SplitDividerProps = {
  /** Workspace width available after the main sidebar yields. */
  availableWidthPx: () => number;
  /** Live conversation column width, including drag preview. */
  chatWidthPx: () => number;
  onBegin: () => ResizeSession | null;
  onReset: () => void;
  /** Stage column is left of the divider. */
  stageOnLeft: () => boolean;
};

export function SplitDivider(props: SplitDividerProps) {
  let el: HTMLDivElement | undefined;
  let cancelActive: (() => void) | undefined;

  const clampChat = (px: number) => clampChatWidthPx(px, props.availableWidthPx());
  /** The separator's value is the stage share, in percent. */
  const percentFor = (chatPx: number) =>
    Math.round(stageShare(chatPx, props.availableWidthPx()) * 100);

  const chatWidthFromClientX = (clientX: number): number => {
    const rect = el?.parentElement?.getBoundingClientRect();
    if (!rect || rect.width <= DIVIDER_PX) return props.chatWidthPx();
    // Sidebar collapse can move either host edge during a drag.
    const host = rect.width;
    const usable = host - DIVIDER_PX;
    const stagePx = props.stageOnLeft()
      ? clientX - rect.left - DIVIDER_PX / 2
      : rect.right - clientX - DIVIDER_PX / 2;
    const chatPx = usable - stagePx;
    // Dragging the conversation below half its floor hides it.
    if (dragRequestsHide(chatPx, CHAT_COL_MIN)) return PANE_HIDDEN_PX;
    return clampChat(snapChatWidthPx(chatPx, host));
  };

  const onPointerDown = (e: PointerEvent) => {
    if (e.button !== 0) return;
    e.preventDefault();
    const handle = el;
    if (!handle) return;
    cancelActive?.();
    const session = props.onBegin();
    if (!session) return;
    const startX = e.clientX;
    cancelActive = startPointerResize({
      handle,
      pointerId: e.pointerId,
      startPosition: startX,
      positionOf: (event) => event.clientX,
      valueAt: chatWidthFromClientX,
      session,
      rootClass: "den-shell--split-resizing",
    });
  };

  const commitChatWidth = (px: number) => {
    const session = props.onBegin();
    if (!session) return;
    session.preview(px);
    session.commit();
  };

  /** Moves the divider: the stage gains `deltaPx` and the conversation loses it. */
  const nudge = (deltaPx: number) => {
    const sign = props.stageOnLeft() ? 1 : -1;
    commitChatWidth(clampChat(props.chatWidthPx() - sign * deltaPx));
  };

  const widestChat = () => maxChatWidthPx(props.availableWidthPx());

  const onKeyDown = (e: KeyboardEvent) => {
    switch (e.key) {
      case "ArrowLeft":
        e.preventDefault();
        nudge(e.shiftKey ? -64 : -16);
        break;
      case "ArrowRight":
        e.preventDefault();
        nudge(e.shiftKey ? 64 : 16);
        break;
      case "Home":
        e.preventDefault();
        if (Number.isFinite(widestChat())) commitChatWidth(widestChat());
        break;
      case "End":
        e.preventDefault();
        commitChatWidth(CHAT_COL_MIN);
        break;
      case "Enter":
        e.preventDefault();
        props.onReset();
        break;
      default:
        break;
    }
  };

  createEffect(on(props.stageOnLeft, () => cancelActive?.(), { defer: true }));
  onCleanup(() => cancelActive?.());

  return (
    <div
      ref={el}
      class="den-split-divider"
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize split"
      aria-valuenow={percentFor(props.chatWidthPx())}
      aria-valuemin={percentFor(widestChat())}
      aria-valuemax={percentFor(CHAT_COL_MIN)}
      tabindex={0}
      data-testid="split-divider"
      onPointerDown={onPointerDown}
      onDblClick={(e) => {
        e.preventDefault();
        props.onReset();
      }}
      onKeyDown={onKeyDown}
    />
  );
}
