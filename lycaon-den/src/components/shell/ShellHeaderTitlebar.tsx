import { ShowLatest } from "../primitives/ShowLatest.tsx";
import {
  StageEdgeControls,
  stageEdgeHasControl,
  type StageEdgePanes,
} from "../nav/StageEdgeControls.tsx";
import { ChromeDragSurface } from "./ChromeDragSurface.tsx";
import { StageBackChip, type StageBack } from "./StageBackChip.tsx";
import { WindowControls } from "./WindowControls.tsx";

type Props = {
  back?: StageBack | null;
  /** Panes that reopen from this title bar's window edges. */
  edges?: StageEdgePanes;
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
  return (
    <div class="den-shell-header-titlebar">
      <ChromeDragSurface />
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
      <div class="den-shell-header-drawers">
        <WindowControls />
      </div>
    </div>
  );
}
