import { Show, createEffect, createSignal, onCleanup } from "solid-js";
import { DenButton } from "../../components/primitives/DenButton.tsx";
import type { FileUndo } from "../commands/file-mutations.ts";
import {
  dismissFileUndoToast,
  getFileUndoToast,
  subscribeFileUndoToast,
} from "./file-undo-toast.ts";

type Props = {
  projectId: string;
  onUndo: (undo: FileUndo[]) => void;
};

export function FileUndoToast(props: Props) {
  const [toast, setToast] = createSignal(getFileUndoToast(props.projectId));
  createEffect(() => setToast(getFileUndoToast(props.projectId)));
  onCleanup(
    subscribeFileUndoToast((id) => {
      if (id === props.projectId.trim()) {
        setToast(getFileUndoToast(props.projectId));
      }
    }),
  );

  return (
    <Show keyed when={toast()}>
      {(held) => (
        <div
          class="den-file-undo-toast"
          data-testid="file-undo-toast"
          role="status"
        >
          <span class="den-file-undo-toast__label">{held.label}</span>
          <DenButton
            variant="primary"
            compact
            data-testid="file-undo-action"
            onClick={() => {
              const undo = held.undo;
              dismissFileUndoToast(props.projectId, held.id);
              props.onUndo(undo);
            }}
          >
            Undo
          </DenButton>
        </div>
      )}
    </Show>
  );
}
