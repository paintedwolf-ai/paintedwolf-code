import { createUniqueId, For, Show } from "solid-js";
import type { SessionMode } from "../../api/types.ts";

const options = [
  { mode: "agent", label: "Agent" },
  { mode: "ask", label: "Ask" },
] as const;

type Props = {
  mode: SessionMode;
  started?: boolean;
  disabled?: boolean;
  upgrade?: "sending" | "reconciling";
  onSelect: (mode: SessionMode) => void;
};

export function SessionModeSwitch(props: Props) {
  const helpId = createUniqueId();
  const statusId = createUniqueId();
  const buttons = new Map<SessionMode, HTMLButtonElement>();
  const disabled = (mode: SessionMode) => Boolean(
    props.disabled || props.upgrade === "sending" || (props.started && mode === "ask"),
  );

  const select = (mode: SessionMode) => {
    if (!disabled(mode)) props.onSelect(mode);
  };

  const navigate = (event: KeyboardEvent, mode: SessionMode) => {
    if (!["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const target = event.key === "Home" ? "agent"
      : event.key === "End" ? "ask"
      : mode === "agent" ? "ask" : "agent";
    if (disabled(target)) return;
    buttons.get(target)?.focus();
    select(target);
  };

  return (
    <div class="den-session-mode">
      <div
        class="den-session-mode__options"
        role="radiogroup"
        aria-label="Session mode"
        aria-describedby={props.upgrade ? `${helpId} ${statusId}` : helpId}
        aria-busy={props.upgrade ? "true" : undefined}
      >
        <For each={options}>
          {(option) => (
            <button
              ref={(button) => { buttons.set(option.mode, button); }}
              type="button"
              role="radio"
              aria-checked={props.mode === option.mode}
              aria-disabled={disabled(option.mode) || undefined}
              tabIndex={(props.started ? "agent" : props.mode) === option.mode ? 0 : -1}
              onClick={() => select(option.mode)}
              onKeyDown={(event) => navigate(event, option.mode)}
            >
              {option.label}
            </button>
          )}
        </For>
      </div>
      <span id={helpId} class="den-session-mode__help">
        Ask uses read-only tools. Connected tools rely on their provider’s read-only declaration.
      </span>
      <Show when={props.upgrade}>
        <span id={statusId} class="den-session-mode__status" role="status">
          {props.upgrade === "sending"
            ? "Switching to Agent…"
            : "Confirming Agent mode. Select Agent to retry."}
        </span>
      </Show>
    </div>
  );
}
