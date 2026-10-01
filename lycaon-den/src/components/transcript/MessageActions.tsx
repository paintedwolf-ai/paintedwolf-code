import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { Show, children, createSignal, onCleanup, type JSX } from "solid-js";
import { focusWithoutScroll } from "../../platform/interaction/focus.ts";

export type MessageActionsProps = {
  onEdit?: () => void;
  onRewind?: () => void;
  onCopy?: () => void;
  /** Edit and rewind wait for the session to go idle. */
  recoveryHeld?: boolean;
  /** Shown ahead of the buttons, such as when the message was sent. */
  leading?: JSX.Element;
};

const COPIED_FLASH_MS = 1200;

function moveToolbarFocus(root: HTMLElement, delta: number) {
  const buttons = [...root.querySelectorAll<HTMLButtonElement>("button")];
  const current = buttons.indexOf(document.activeElement as HTMLButtonElement);
  if (current < 0) return;
  const next = buttons[(current + delta + buttons.length) % buttons.length];
  focusWithoutScroll(next);
}

export function MessageActions(props: MessageActionsProps) {
  const [activeAction, setActiveAction] = createSignal("copy");
  const firstAction = () => props.onCopy ? "copy" : props.onEdit ? "edit" : "rewind";
  const tabIndex = (action: string) => {
    const available = { copy: props.onCopy, edit: props.onEdit, rewind: props.onRewind };
    const selected = available[activeAction() as keyof typeof available] ? activeAction() : firstAction();
    return selected === action ? 0 : -1;
  };
  const [copied, setCopied] = createSignal(false);
  let copiedTimer: ReturnType<typeof setTimeout> | undefined;
  onCleanup(() => {
    if (copiedTimer) clearTimeout(copiedTimer);
  });

  const leading = children(() => props.leading);

  const copy = () => {
    props.onCopy?.();
    setCopied(true);
    if (copiedTimer) clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => {
      copiedTimer = undefined;
      setCopied(false);
    }, COPIED_FLASH_MS);
  };

  return (
    <div
      class="den-msg-actions"
      role="toolbar"
      aria-label="Message actions"
      data-testid="message-actions"
      onFocusIn={e => { const action = (e.target as HTMLElement).closest<HTMLElement>("[data-message-action]")?.dataset.messageAction; if (action) setActiveAction(action); }}
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
      <Show when={leading()}>
        {leading()}
        <span class="den-msg-actions__sep" aria-hidden="true" />
      </Show>
      <Show when={props.onCopy}>
        <button
          type="button"
          class="den-msg-actions__btn den-inset-icon-btn"
          data-testid="bubble-copy"
          data-message-action="copy"
          tabindex={tabIndex("copy")}
          aria-label={copied() ? "Copied" : "Copy"}
          onClick={copy}
        >
          <Show when={copied()} fallback={<ThemeIcon slot="copy" size={14} />}>
            <ThemeIcon slot="check" size={14} />
          </Show>
        </button>
      </Show>
      <Show when={props.onEdit}>
        <button
          type="button"
          class="den-msg-actions__btn den-inset-icon-btn"
          data-testid="user-bubble-edit"
          data-message-action="edit"
          tabindex={tabIndex("edit")}
          aria-label="Edit from here"
          aria-disabled={props.recoveryHeld ? "true" : undefined}
          data-tip={props.recoveryHeld ? "Edit after this turn finishes" : "Edit from here"}
          data-tip-pos="below"
          onClick={() => {
            if (!props.recoveryHeld) props.onEdit?.();
          }}
        >
          <ThemeIcon slot="edit" size={14} />
        </button>
      </Show>
      <Show when={props.onRewind}>
        <button
          type="button"
          class="den-msg-actions__btn den-msg-actions__btn--danger den-inset-icon-btn"
          data-testid="user-bubble-rewind"
          data-message-action="rewind"
          tabindex={tabIndex("rewind")}
          aria-label="Rewind to here"
          aria-disabled={props.recoveryHeld ? "true" : undefined}
          data-tip={props.recoveryHeld ? "Rewind after this turn finishes" : "Rewind to here"}
          data-tip-pos="below"
          onClick={() => {
            if (!props.recoveryHeld) props.onRewind?.();
          }}
        >
          <ThemeIcon slot="rewind" size={14} />
        </button>
      </Show>
    </div>
  );
}
