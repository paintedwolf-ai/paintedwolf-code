import { dragWindowOnMove } from "../../platform/windows/window-drag-gesture.ts";

/** Return target for a routed jump; direct sidebar visits have no return target. */
export type StageBack = {
  label: string;
  onBack: () => void;
  testId?: string;
};

/** Routed-return affordance shared by stage chrome and Shell titlebars. */
export function StageBackChip(props: { back: StageBack }) {
  let draggedDuringPress = false;

  return (
    <button
      type="button"
      class="den-stage-back"
      data-testid={props.back.testId ?? "stage-back"}
      onPointerDown={(e) => {
        draggedDuringPress = false;
        dragWindowOnMove(e, () => {
          draggedDuringPress = true;
        });
      }}
      onClick={() => {
        if (draggedDuringPress) {
          draggedDuringPress = false;
          return;
        }
        props.back.onBack();
      }}
    >
      {props.back.label}
    </button>
  );
}
