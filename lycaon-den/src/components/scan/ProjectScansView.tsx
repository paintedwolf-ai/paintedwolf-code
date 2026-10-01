import { onMount } from "solid-js";
import type { ProjectRoot } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { requestFirstTimeTip } from "../../first-time-tips/first-time-tips-service.ts";
import { SecurityFindingsPane } from "./SecurityFindingsPane.tsx";

type Props = {
  projectId: string;
  appStore: AppStore;
  /** Root used for repository-relative finding paths. */
  repoRoot?: string;
  /** Every attached root, for handing a finding's file to the agent. */
  roots?: readonly ProjectRoot[];
  /** Back navigation for routed entries. */
  back?: import("../shell/StageBackChip.tsx").StageBack | null;
};

/** Security stage for scan history and finding details. */
export function ProjectScansView(props: Props) {
  onMount(() => requestFirstTimeTip("project-security"));

  return (
    <div class="project-scans-view" data-testid="project-scans-view">
      <SecurityFindingsPane
        appStore={props.appStore}
        back={props.back}
        projectId={props.projectId}
        repoRoot={props.repoRoot}
        roots={props.roots}
        latestScanId={props.appStore.state.latestCodeScan?.scan_id}
        liveScan={props.appStore.state.latestCodeScan}
      />
    </div>
  );
}
