import {
  Index,
  Show,
  children,
  createEffect,
  createSignal,
  on,
  onCleanup,
  type Accessor,
  type JSX,
} from "solid-js";
import type { IconSlot } from "../../contributions/theme-vocabulary.generated.ts";
import { focusWithoutScroll } from "../../platform/interaction/focus.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { MessageTime } from "./TranscriptTimeRows.tsx";

export type MessageActionAlignment = "start" | "end";

export type MessageActionTone = "default" | "danger";

export type MessageActionFeedback = {
  label: string;
  icon: IconSlot;
  tip?: string;
};

export type MessageActionItem = {
  /** Stable key within the toolbar for roving focus. */
  id: string;
  /** Accessible name read by assistive tech. */
  label: string;
  /** Themeable icon slot rendered inside the button. */
  icon: IconSlot;
  /** Brief confirmation state shown after activation (such as Copy → Copied). */
  feedback?: MessageActionFeedback;
  /** Optional hover tooltip text, positioned below the toolbar. */
  tip?: string;
  /** Keeps the button mounted in place with `aria-disabled="true"` while unavailable. */
  disabled?: boolean;
  /** Visual emphasis for truncating or destructive actions. */
  tone?: MessageActionTone;
  /** Optional `data-testid` for queries and harness helpers. */
  testId?: string;
  onSelect: () => void;
};

export type MessageActionsProps = {
  /** Message timestamp rendered as relative "time ago" ahead of the buttons. */
  ts?: string;
  /** Horizontal anchor under the message bubble: `"end"` (bottom-right) or `"start"` (bottom-left). */
  align?: MessageActionAlignment;
  /** Optional custom leading element ahead of the buttons; overrides `ts` when provided. */
  leading?: JSX.Element;
  /** Relevant action buttons for this message, rendered in order. */
  actions: readonly MessageActionItem[];
};

export function copyMessageAction(onCopy: () => void): MessageActionItem {
  return {
    id: "copy",
    label: "Copy",
    icon: "copy",
    feedback: { label: "Copied", icon: "check" },
    testId: "bubble-copy",
    onSelect: onCopy,
  };
}

export type UserMessageActionOptions = {
  onCopy?: () => void;
  onEdit?: () => void;
  onRewind?: () => void;
  /** Edit and rewind wait for the session to go idle. */
  recoveryHeld?: boolean;
};

export function buildUserMessageActions(
  options: UserMessageActionOptions,
): MessageActionItem[] {
  const items: MessageActionItem[] = [];
  if (options.onCopy) {
    items.push(copyMessageAction(options.onCopy));
  }
  if (options.onEdit) {
    const held = options.recoveryHeld === true;
    items.push({
      id: "edit",
      label: "Edit from here",
      icon: "edit",
      tip: held ? "Edit after this turn finishes" : "Edit from here",
      disabled: held,
      testId: "user-bubble-edit",
      onSelect: options.onEdit,
    });
  }
  if (options.onRewind) {
    const held = options.recoveryHeld === true;
    items.push({
      id: "rewind",
      label: "Rewind to here",
      icon: "rewind",
      tip: held ? "Rewind after this turn finishes" : "Rewind to here",
      disabled: held,
      tone: "danger",
      testId: "user-bubble-rewind",
      onSelect: options.onRewind,
    });
  }
  return items;
}

export type AssistantMessageActionOptions = {
  onCopy?: () => void;
};

export function buildAssistantMessageActions(
  options: AssistantMessageActionOptions,
): MessageActionItem[] {
  const items: MessageActionItem[] = [];
  if (options.onCopy) {
    items.push(copyMessageAction(options.onCopy));
  }
  return items;
}

const ACTION_FEEDBACK_MS = 1200;

function moveToolbarFocus(root: HTMLElement, delta: number) {
  const buttons = [...root.querySelectorAll<HTMLButtonElement>("button")];
  const current = buttons.indexOf(document.activeElement as HTMLButtonElement);
  if (current < 0) return;
  const next = buttons[(current + delta + buttons.length) % buttons.length];
  focusWithoutScroll(next);
}

function MessageActionButton(props: {
  item: Accessor<MessageActionItem>;
  selected: boolean;
}) {
  const [flashing, setFlashing] = createSignal(false);
  let flashTimer: ReturnType<typeof setTimeout> | undefined;

  const clearFlash = () => {
    if (flashTimer) {
      clearTimeout(flashTimer);
      flashTimer = undefined;
    }
    setFlashing(false);
  };

  createEffect(
    on(
      () => props.item().id,
      () => clearFlash(),
      { defer: true },
    ),
  );

  onCleanup(() => {
    if (flashTimer) clearTimeout(flashTimer);
  });

  const activeFeedback = () => (flashing() ? props.item().feedback : undefined);
  const label = () => activeFeedback()?.label ?? props.item().label;
  const icon = () => activeFeedback()?.icon ?? props.item().icon;
  const tip = () => activeFeedback()?.tip ?? props.item().tip;

  const handleClick = () => {
    const current = props.item();
    if (current.disabled) return;
    current.onSelect();
    if (!current.feedback) return;
    setFlashing(true);
    if (flashTimer) clearTimeout(flashTimer);
    flashTimer = setTimeout(() => {
      flashTimer = undefined;
      setFlashing(false);
    }, ACTION_FEEDBACK_MS);
  };

  return (
    <button
      type="button"
      class="den-msg-actions__btn den-inset-icon-btn"
      classList={{ "den-msg-actions__btn--danger": props.item().tone === "danger" }}
      data-testid={props.item().testId}
      data-message-action={props.item().id}
      tabindex={props.selected ? 0 : -1}
      aria-label={label()}
      aria-disabled={props.item().disabled ? "true" : undefined}
      data-tip={tip()}
      data-tip-pos={tip() ? "below" : undefined}
      onClick={handleClick}
    >
      <ThemeIcon slot={icon()} size={14} />
    </button>
  );
}

export function MessageActions(props: MessageActionsProps) {
  const [activeAction, setActiveAction] = createSignal<string | undefined>(undefined);
  const selectedActionId = () => {
    const current = activeAction();
    if (current && props.actions.some((item) => item.id === current)) return current;
    return props.actions[0]?.id;
  };

  const customLeading = children(() => props.leading);
  const validTimestamp = () => {
    const ts = props.ts;
    return ts && Number.isFinite(Date.parse(ts)) ? ts : undefined;
  };
  const hasLeading = () => Boolean(customLeading()) || validTimestamp() !== undefined;
  const align = () => props.align ?? "end";

  return (
    <Show when={props.actions.length > 0 || hasLeading()}>
      <div
        class="den-msg-actions"
        classList={{ "den-msg-actions--start": align() === "start" }}
        data-align={align()}
        role="toolbar"
        aria-label="Message actions"
        data-testid="message-actions"
        onFocusIn={(e) => {
          const action = (e.target as HTMLElement).closest<HTMLElement>(
            "[data-message-action]",
          )?.dataset.messageAction;
          if (action) setActiveAction(action);
        }}
        onKeyDown={(e) => {
          if (e.key === "ArrowRight") {
            e.preventDefault();
            moveToolbarFocus(e.currentTarget, 1);
          } else if (e.key === "ArrowLeft") {
            e.preventDefault();
            moveToolbarFocus(e.currentTarget, -1);
          }
        }}
      >
        <Show
          when={customLeading()}
          fallback={
            <Show when={validTimestamp()}>
              {(ts) => <MessageTime ts={ts()} placement="toolbar" />}
            </Show>
          }
        >
          {customLeading()}
        </Show>
        <Show when={hasLeading() && props.actions.length > 0}>
          <span class="den-msg-actions__sep" aria-hidden="true" />
        </Show>
        <Index each={props.actions}>
          {(item) => (
            <MessageActionButton
              item={item}
              selected={selectedActionId() === item().id}
            />
          )}
        </Index>
      </div>
    </Show>
  );
}
