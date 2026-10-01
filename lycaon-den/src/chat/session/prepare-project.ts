import { refreshBoard } from "../actions/board-actions.ts";
import {
  getLycaonClient,
  projectEventsAreSubscribed,
  subscribeProjectEvents,
  waitForProjectEvents,
} from "../../platform/connection/app-connection.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { ProjectsStore } from "../../store/projects-store.ts";

/** Wait for project events; fetch board and git on a new stream. */
export async function prepareProjectScope(
  appStore: AppStore,
  projects: ProjectsStore,
  projectId: string,
): Promise<void> {
  const client = getLycaonClient();
  if (!client) return;
  const alreadyLive = projectEventsAreSubscribed(projectId);
  subscribeProjectEvents(appStore, projectId);
  if (!(await waitForProjectEvents(projectId))) {
    throw new Error("Project event stream did not connect.");
  }
  if (alreadyLive) return;
  const project = projects.byId(projectId);
  const boardPath =
    project?.roots.find((r) => r.is_primary)?.path ??
    project?.roots[0]?.path ??
    "";
  if (!boardPath) return;
  void refreshBoard(appStore, client, boardPath, projects.state.projects, undefined, {
    includeGit: true,
  }).catch(() => undefined);
}
