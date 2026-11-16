import { Show } from "solid-js";
import type { SlotContent } from "../../ui/slot-content.ts";
import { ShowLatest } from "../primitives/ShowLatest.tsx";
import {
  StageEdgeControls,
  stageEdgeHasControl,
  type StageEdgePanes,
} from "../nav/StageEdgeControls.tsx";
import { ChromeDragSurface } from "./ChromeDragSurface.tsx";
import { StageBackChip, type StageBack } from "./StageBackChip.tsx";
import { WindowControls, type WindowEdgeClaim } from "./WindowControls.tsx";

type Props = {
  back?: StageBack | null;
  /** Panes that reopen from this title bar's window edges. */
  edges?: StageEdgePanes;
  /** Window edges whose drawn controls this title bar carries. */
  windowEdges?: readonly WindowEdgeClaim[];
  /** The session's tabs, carried here while its conversation is off screen. */
  sessionTabs?: SlotContent;
};

/** Stage title bar: back chip, pane reopen controls, and the session's tabs. */
export function ShellHeaderTitlebar(props: Props) {
  const panesAtEdge = (edge: "left" | "right") => {
    const panes = props.edges;
    return panes && stageEdgeHasControl(panes, edge) ? panes : undefined;
  };
  const backChip = () => (
    <ShowLatest when={props.back}>
      {(back) => <StageBackChip back={back()} />}
    </ShowLatest>
  );
  const windowEdge = (side: "left" | "right") =>
    props.windowEdges?.find((claim) => claim.side === side);
  return (
    <div class="den-shell-header-titlebar">
      <ChromeDragSurface />
      <ShowLatest when={windowEdge("left")}>
        {(claim) => <WindowControls {...claim()} />}
      </ShowLatest>
      <ShowLatest when={panesAtEdge("left")} fallback={backChip()}>
        {(panes) => (
          <div class="den-shell-header-titlebar__edge" data-edge="left">
            <StageEdgeControls
              context={panes().context}
              nav={panes().nav}
              conversation={panes().conversation}
              edge="left"
            />
            {backChip()}
          </div>
        )}
      </ShowLatest>
      <ShowLatest when={panesAtEdge("right")}>
        {(panes) => (
          <div
            class="den-shell-header-titlebar__edge den-shell-header-titlebar__edge--trailing"
            data-edge="right"
          >
            <StageEdgeControls
              context={panes().context}
              nav={panes().nav}
              conversation={panes().conversation}
              edge="right"
            />
          </div>
        )}
      </ShowLatest>
      <Show when={props.sessionTabs?.present()}>
        <div class="den-shell-header-titlebar__tabs">
          {props.sessionTabs?.children()}
        </div>
      </Show>
      <ShowLatest when={windowEdge("right")}>
        {(claim) => <WindowControls {...claim()} />}
      </ShowLatest>
    </div>
  );
}
