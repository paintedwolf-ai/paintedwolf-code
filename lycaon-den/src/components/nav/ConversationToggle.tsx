import { ariaKeyShortcutsForHandler } from "../../shortcuts/display-binding-for.ts";
import { Show } from "solid-js";
import type { AttentionRow } from "../../api/types.ts";
import { attentionReasonLabel } from "../../attention/attention-model.ts";
import { SidebarCollapseIcon, SidebarExpandIcon } from "./NavSidebarToggle.tsx";
import { PaneAttentionDot, paneAttentionMark, paneToggleTip } from "./PaneToggle.tsx";

type ToggleProps = {
  onClick: () => void;
  /** Window edge the conversation occupies in the split. */
  side: "left" | "right";
};

/** Hides the split's conversation — sits at the seam end of its tab rail. */
export function ConversationCollapseButton(props: ToggleProps) {
  return (
    <button
      type="button"
      class="den-inset-icon-btn den-pane-toggle den-pane-toggle--hide-conversation"
      data-testid="conversation-collapse-btn"
      aria-label="Hide conversation"
      data-tip={paneToggleTip("Hide conversation", "layout.toggleConversation")}
      aria-keyshortcuts={ariaKeyShortcutsForHandler("layout.toggleConversation")}
      onClick={() => props.onClick()}
    >
      <SidebarCollapseIcon side={props.side} />
    </button>
  );
}

/** Restores the conversation with its attention marker. */
export function ConversationExpandButton(
  props: ToggleProps & { attention?: AttentionRow; labelled?: boolean },
) {
  const marked = () => paneAttentionMark(props.attention);
  const label = () => {
    const row = marked();
    return row
      ? `Show conversation — ${attentionReasonLabel(row)}`
      : "Show conversation";
  };
  return (
    <button
      type="button"
      class="den-inset-icon-btn den-pane-toggle"
      classList={{ "den-pane-toggle--labelled": props.labelled }}
      data-testid="conversation-expand-btn"
      aria-label={label()}
      data-tip={paneToggleTip(label(), "layout.toggleConversation")}
      aria-keyshortcuts={ariaKeyShortcutsForHandler("layout.toggleConversation")}
      onClick={() => props.onClick()}
    >
      <SidebarExpandIcon side={props.side} />
      <Show when={props.labelled}><span>Chat</span></Show>
      <PaneAttentionDot row={marked()} />
    </button>
  );
}
