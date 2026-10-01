import { Show, createEffect, createSignal, onCleanup } from "solid-js";
import type { AttentionRow } from "../../api/types.ts";
import { attentionReasonLabel } from "../../attention/attention-model.ts";
import { SidebarAutomaticExpandIcon } from "./NavSidebarToggle.tsx";
import { PaneAttentionDot, paneAttentionMark } from "./PaneToggle.tsx";

export type SplitFitRestoreProps = {
  /** Column the host is too narrow to carry. */
  pane: "conversation" | "stage";
  /** How that column is named in the label — `conversation`, or a stage. */
  label: string;
  /** Window edge the column occupies in the split. */
  side: "left" | "right";
  attention?: AttentionRow;
  labelled?: boolean;
  /** Refusals of the shared width claim; a rise marks one more. */
  refusals: () => number;
  onClick: () => void;
};

const NO_ROOM_LABEL = "No room to widen — the window already fills the display";

/** Outlasts the nudge, and clears it where motion is reduced. */
const NUDGE_CLEAR_MS = 160;

/** Claims the width a narrow split needs for the column it gave up. */
export function SplitFitRestoreButton(props: SplitFitRestoreProps) {
  const marked = () => paneAttentionMark(props.attention);
  const [nudging, setNudging] = createSignal(false);
  let seen = 0;
  let clearNudge: ReturnType<typeof setTimeout> | undefined;

  // Every refusal gets its own nudge, including a repeat attempt.
  createEffect(() => {
    const count = props.refusals();
    const refused = count > seen;
    seen = count;
    if (!refused) return;
    setNudging(true);
    clearTimeout(clearNudge);
    clearNudge = setTimeout(() => setNudging(false), NUDGE_CLEAR_MS);
  });
  onCleanup(() => clearTimeout(clearNudge));

  // Keep the refusal reason visible after the nudge.
  const label = () => {
    const row = marked();
    const reason = row ? ` — ${attentionReasonLabel(row)}` : "";
    if (props.refusals() > 0) return `${NO_ROOM_LABEL}${reason}`;
    return `Widen window to show ${props.label}${reason}`;
  };
  return (
    <button
      type="button"
      class="den-inset-icon-btn den-pane-toggle den-split-fit-restore"
      classList={{ "den-pane-toggle--labelled": props.labelled }}
      data-testid={`split-fit-restore-${props.pane}`}
      data-refused={nudging() ? props.side : undefined}
      aria-label={label()}
      data-tip={label()}
      onClick={() => props.onClick()}
    >
      <SidebarAutomaticExpandIcon side={props.side} />
      <Show when={props.labelled}><span>{props.pane === "conversation" ? "Chat" : "Context"}</span></Show>
      <PaneAttentionDot row={marked()} />
    </button>
  );
}
