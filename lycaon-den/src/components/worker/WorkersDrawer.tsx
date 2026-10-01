import type { WorkerTask } from "../../api/types.ts";
import { workerAgentLabel, type WorkerDrawerFocus } from "../../chat/worker/workers-model.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { ContextDrawer } from "../shell/ContextDrawer.tsx";
import { WorkerTranscript } from "./WorkerTranscript.tsx";

type Props = {
  open: boolean;
  appStore: AppStore;
  projectDir: string;
  workers: WorkerTask[];
  selectedId: string | null;
  scrollToFocus?: WorkerDrawerFocus;
  onScrollToFocusHandled?: () => void;
  onClose: () => void;
};

// Worker selection belongs to the Workers tab.
export function WorkersDrawer(props: Props) {
  const selected = () =>
    props.workers.find((w) => w.id === props.selectedId) ?? null;

  return (
    <ContextDrawer
      open={props.open}
      testId="workers-drawer"
      titleId="workers-drawer-title"
      title={selected() ? workerAgentLabel(selected() as WorkerTask) : "Worker"}
      closeLabel="Close worker"
      onClose={() => props.onClose()}
      scrollTestId="worker-transcript"
    >
      <WorkerTranscript
        workerId={props.selectedId}
        workers={props.workers}
        appStore={props.appStore}
        projectDir={props.projectDir}
        scrollToFocus={props.scrollToFocus}
        onScrollToFocusHandled={props.onScrollToFocusHandled}
      />
    </ContextDrawer>
  );
}
