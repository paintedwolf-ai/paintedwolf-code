import { Show } from "solid-js";
import type { AttentionRow } from "../../api/types.ts";
import {
  NavSidebarAutoRestoreButton,
  NavSidebarExpandButton,
} from "./NavSidebarToggle.tsx";
import { ConversationExpandButton } from "./ConversationToggle.tsx";
import { SplitFitRestoreButton } from "./SplitFitRestore.tsx";

import { ContextPaneToggle } from "./ContextPaneToggle.tsx";

type Side = "left" | "right";

export type StageEdgePanes = {
  context?: { side: Side; onHide: () => void };
  nav: {
    collapsed: boolean;
    automatic: boolean;
    /** The conversation handles sidebar restore while both split columns fit. */
    onlyWhenNarrow?: boolean;
    /** Window edge the sidebar occupies. */
    side: Side;
    onExpand: () => void;
  };
  conversation: {
    collapsed: boolean;
    /** Window edge the conversation occupies in the split. */
    side: Side;
    attention?: AttentionRow;
    onExpand: () => void;
    /** A narrow host keeps the stage; the band decides when this shows. */
    hiddenWhenNarrow: boolean;
    onWiden: () => void;
    /** Refusals of that width claim, marked on the control that made it. */
    widenRefusals: () => number;
  };
};

/** A hidden pane reopens from the stage title bar's edge on its own side. */
export function stageEdgeHasControl(panes: StageEdgePanes, edge: Side): boolean {
  return (
    (panes.context != null && panes.context.side !== edge) ||
    (panes.nav.collapsed && panes.nav.side === edge) ||
    ((panes.conversation.collapsed || panes.conversation.hiddenWhenNarrow) &&
      panes.conversation.side === edge)
  );
}

/** Reopen controls for the panes hidden at one window edge. */
export function StageEdgeControls(props: StageEdgePanes & { edge: Side }) {
  const conversationAtEdge = () => props.conversation.side === props.edge;
  const paired = () => props.nav.collapsed && props.nav.side === props.conversation.side &&
    (props.conversation.collapsed || props.conversation.hiddenWhenNarrow);
  return (
    <>
      <Show when={props.context?.side !== props.edge ? props.context : undefined} keyed>
        {(context) => <ContextPaneToggle side={context.side} onClick={() => context.onHide()} />}
      </Show>
      <Show when={props.nav.collapsed && props.nav.side === props.edge}>
        <span
          class="den-stage-nav-restore"
          classList={{ "den-nav-split-fallback": props.nav.onlyWhenNarrow }}
        >
          <Show
            when={props.nav.automatic}
            fallback={
              <NavSidebarExpandButton
                side={props.nav.side}
                labelled={paired()}
                onClick={() => props.nav.onExpand()}
              />
            }
          >
            <NavSidebarAutoRestoreButton
              side={props.nav.side}
              labelled={paired()}
              onClick={() => props.nav.onExpand()}
            />
          </Show>
        </span>
      </Show>
      <Show when={props.conversation.collapsed && conversationAtEdge()}>
        <ConversationExpandButton
          side={props.conversation.side}
          labelled={paired()}
          attention={props.conversation.attention}
          onClick={() => props.conversation.onExpand()}
        />
      </Show>
      <Show when={props.conversation.hiddenWhenNarrow && conversationAtEdge()}>
        <SplitFitRestoreButton
          pane="conversation"
          label="conversation"
          side={props.conversation.side}
          labelled={paired()}
          attention={props.conversation.attention}
          refusals={props.conversation.widenRefusals}
          onClick={() => props.conversation.onWiden()}
        />
      </Show>
    </>
  );
}
