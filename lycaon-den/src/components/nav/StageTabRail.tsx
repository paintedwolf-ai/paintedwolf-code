import type { OpenWorkerOptions } from "../../chat/worker/workers-model.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { TabId } from "../chatview/tabs.ts";
import { createSessionTabs } from "./session-tabs.tsx";
import { TabBar } from "./TabBar.tsx";

type StageTabRailProps = {
  appStore: AppStore;
  sessionId: string;
  selectedWorkerId: string | null;
  onOpenWorker: (workerId?: string, opts?: OpenWorkerOptions) => void;
  worklogOpen: boolean;
  onOpenWorklog: () => void;
  openTab: TabId | null;
  onSelectTab: (next: TabId | null) => void;
  /** Puts the conversation on screen for an action that continues there. */
  onShowConversation: () => void;
};

/** The session's tabs in a stage title bar, while its conversation is off screen. */
export function StageTabRail(props: StageTabRailProps) {
  const { tabs } = createSessionTabs({
    appStore: props.appStore,
    sessionId: () => props.sessionId,
    selectedWorkerId: () => props.selectedWorkerId,
    onOpenWorker: (workerId, opts) => props.onOpenWorker(workerId, opts),
    worklogOpen: () => props.worklogOpen,
    onOpenWorklog: () => props.onOpenWorklog(),
    showConversation: () => props.onShowConversation(),
  });
  return (
    <TabBar
      placement="stage"
      tabs={tabs()}
      open={props.openTab}
      onOpenChange={props.onSelectTab}
    />
  );
}
