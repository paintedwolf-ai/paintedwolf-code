import {
  NAV_WIDTH_MIN_PX,
} from "../../../shared/app-state-types.ts";
import { dragRequestsHide, PANE_HIDDEN_PX } from "../../layout/drag-to-hide.ts";
import { beginNavWidthResize, resetNavWidthPx } from "../../shell/workspace-layout.ts";
import {
  clampNavWidthPx,
  maxNavWidthPx,
} from "../../shell/shell-layout-model.ts";
import { ResizeHandle } from "../primitives/ResizeHandle.tsx";

type Props = {
  width: number;
  side: "left" | "right";
  /** Releasing a drag past half the minimum width hides the rail. */
  onHide: () => void;
};

/** Drag handle on the main navigation rail's content edge. */
export function NavResizeHandle(props: Props) {
  return (
    <ResizeHandle
      class={`den-shell-nav-resize${props.side === "right" ? " den-shell-nav-resize--right" : ""}`}
      axis="width"
      direction={props.side === "right" ? -1 : 1}
      size={() => props.width}
      clamp={(value) =>
        dragRequestsHide(value, NAV_WIDTH_MIN_PX)
          ? PANE_HIDDEN_PX
          : clampNavWidthPx(value, window.innerWidth)
      }
      onBegin={() => beginNavWidthResize(() => props.onHide())}
      onReset={() => void resetNavWidthPx()}
      rootClass="den-shell--nav-resizing"
      ariaLabel="Resize navigation sidebar"
      ariaMin={() => NAV_WIDTH_MIN_PX}
      ariaMax={() => maxNavWidthPx(window.innerWidth)}
      tip="Double-click to reset width · drag past the edge to hide"
    />
  );
}
