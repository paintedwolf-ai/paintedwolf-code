import { Show } from "solid-js";
import type { JSX } from "solid-js";
import { DenButton } from "./primitives/DenButton.tsx";
import { Scrollport } from "./primitives/Scrollport.tsx";

export type CriticalStopAction = {
  label: string;
  onClick: () => void | Promise<void>;
  disabled?: boolean;
};

export type CriticalStopProps = {
  /** Stable machine id for the condition, e.g. `offline`, `OS_BELOW_FLOOR`. */
  code: string;
  title: string;
  message: string;
  /** Optional remedy line, rendered in the emphasis style below the message. */
  detail?: string;
  /** Recovery action. */
  primaryAction?: CriticalStopAction;
  /** Lower-emphasis action, e.g. saving the diagnostics bundle for a report. */
  secondaryAction?: CriticalStopAction;
  /** Selectable non-sensitive environment facts. */
  facts?: string;
  /** Selectable host failure evidence. */
  diagnostic?: string;
  /** Outcome of the last secondary action. */
  status?: string;
  /** Condition-specific controls, such as a credential-vault password form. */
  children?: JSX.Element;
};

/** Full-window stop. `code` is the machine id for e2e and reports. */
export function CriticalStop(props: CriticalStopProps): JSX.Element {
  return (
    <div
      class="den-critical-stop"
      role="alert"
      data-code={props.code}
      data-testid="critical-stop"
    >
      <p class="den-critical-stop__title">{props.title}</p>
      <p class="den-critical-stop__message">{props.message}</p>
      <Show when={props.detail}>
        <p class="den-critical-stop__detail" data-testid="critical-stop-detail">
          {props.detail}
        </p>
      </Show>
      {props.children}
      <Show when={props.facts}>
        <p class="den-critical-stop__facts" data-testid="critical-stop-facts">
          {props.facts}
        </p>
      </Show>
      <Show when={props.diagnostic}>
        <Scrollport
          class="den-critical-stop__diagnostic"
          contentAs="pre"
          contentClass="den-critical-stop__diagnostic-content"
          data-testid="critical-stop-diagnostic"
        >
          {props.diagnostic}
        </Scrollport>
      </Show>
      <Show when={props.primaryAction} keyed>
        {(action) => (
          <DenButton
            variant="primary"
            data-testid="critical-stop-primary"
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
            data-testid="critical-stop-secondary"
            disabled={action.disabled}
            onClick={() => void action.onClick()}
          >
            {action.label}
          </DenButton>
        )}
      </Show>
      <Show when={props.status}>
        <p class="den-critical-stop__status" data-testid="critical-stop-status">
          {props.status}
        </p>
      </Show>
    </div>
  );
}
