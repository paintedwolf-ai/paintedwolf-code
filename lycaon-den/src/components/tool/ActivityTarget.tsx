import { Show, createSignal } from "solid-js";
import type { ActivityTargetText } from "../../chat/tool/command-activity-source.ts";
import { writeClipboardText } from "../../utils/clipboard.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { RedactedText } from "../transcript/RedactionMark.tsx";

type Props = {
  target: ActivityTargetText;
  /** Further distinct targets folded into the same card. */
  extraCount?: number;
  /** Names what Copy copies, such as "command" or "target". */
  noun: string;
};

/** What an opened activity ran: wrapped monospace text with a copy action. */
export function ActivityTarget(props: Props) {
  const [state, setState] = createSignal("");
  const copy = async () => {
    try { await writeClipboardText(props.target.text); setState("Copied"); }
    catch { setState(`Could not copy ${props.noun}`); }
  };
  return <div class="den-activity-target" data-testid="activity-target">
    <code class="den-activity-target__text">
      <Show when={props.target.redaction} keyed fallback={props.target.text}>
        {(redaction) => <RedactedText message={{ host_secret_redaction: redaction.meta }} field={redaction.field} text={props.target.text} />}
      </Show>
    </code>
    <Show when={(props.extraCount ?? 0) > 0}>
      <span class="den-activity-target__more">and {props.extraCount!.toLocaleString()} more</span>
    </Show>
    <button type="button" class="den-activity-target__copy den-inset-icon-btn" data-testid="activity-target-copy"
      aria-label={state() === "Copied" ? "Copied" : `Copy ${props.noun}`} onClick={() => void copy()}>
      <ThemeIcon slot={state() === "Copied" ? "check" : "copy"} size={13} />
    </button>
    <p class="den-visually-hidden" role="status">{state()}</p>
  </div>;
}
