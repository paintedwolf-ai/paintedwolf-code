import { Show } from "solid-js";
import type { JSX } from "solid-js";
import { DenButton } from "./primitives/DenButton.tsx";

export type SystemNudgeAction = {
  label: string;
  onClick: () => void | Promise<void>;
  disabled?: boolean;
  /** Overrides the default `system-nudge-<role>` test id. */
  testId?: string;
};

export type SystemNudgeProps = {
  title: string;
  description?: JSX.Element;
  primaryAction?: SystemNudgeAction;
  secondaryAction?: SystemNudgeAction;
  /** Optional low-emphasis action (e.g. "open settings"), rendered as a link. */
  tertiaryAction?: SystemNudgeAction;
  /** Dedicated (×) dismiss control, required for all notification cards. */
  onDismiss: () => void;
  /** "status" for a card that reports what happened; default "note". */
  role?: "note" | "status";
  testId?: string;
};

/** Shared inline notification card. */
export function SystemNudge(props: SystemNudgeProps): JSX.Element {
  return (
    <div
      class="system-nudge"
      role={props.role ?? "note"}
      data-testid={props.testId ?? "system-nudge"}
    >
      <div class="system-nudge__text">
        <span class="system-nudge__title">{props.title}</span>
        <Show when={props.description}>
          <div class="system-nudge__sub">{props.description}</div>
        </Show>
      </div>
      <div class="system-nudge__actions">
        <Show when={props.tertiaryAction} keyed>
          {(action) => (
            <DenButton
              variant="link"
              compact
              data-testid={action.testId ?? "system-nudge-tertiary"}
              disabled={action.disabled}
              onClick={() => void action.onClick()}
            >
              {action.label}
            </DenButton>
          )}
        </Show>
        <Show when={props.secondaryAction} keyed>
          {(action) => (
            <DenButton
              variant="secondary"
              compact
              data-testid={action.testId ?? "system-nudge-secondary"}
              disabled={action.disabled}
              onClick={() => void action.onClick()}
            >
              {action.label}
            </DenButton>
          )}
        </Show>
        <Show when={props.primaryAction} keyed>
          {(action) => (
            <DenButton
              variant="primary"
              compact
              data-testid={action.testId ?? "system-nudge-primary"}
              disabled={action.disabled}
              onClick={() => void action.onClick()}
            >
              {action.label}
            </DenButton>
          )}
        </Show>
        <button
          type="button"
          class="system-nudge__close den-inset-icon-btn"
          aria-label="Dismiss"
          data-testid="system-nudge-dismiss"
          onClick={() => props.onDismiss()}
        >
          ×
        </button>
      </div>
    </div>
  );
}
