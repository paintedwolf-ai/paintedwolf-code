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
};

/** Stage title bar for routes without chat tabs. */
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
      <ShowLatest when={windowEdge("right")}>
        {(claim) => <WindowControls {...claim()} />}
      </ShowLatest>
    </div>
  );
}
