import { createEffect, on, type Accessor } from "solid-js";
import type { ChatTabChromeValue } from "../../chat/composer/chat-tab-chrome.tsx";
import { refreshWorkflowState } from "../../chat/workflow/workflow-actions.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import type { Project } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";

export type ChatTabsStateDeps = {
  chrome: ChatTabChromeValue;
  appStore: AppStore;
  projects: readonly Project[];
  sessionId: Accessor<string>;
  projectDir: Accessor<string>;
  offline: Accessor<boolean>;
};

export type ChatTabsState = {
  workflowsOpen: Accessor<boolean>;
  setWorkflowsOpen: (open: boolean) => void;
};

export function createChatTabsState(deps: ChatTabsStateDeps): ChatTabsState {
  const workflowsOpen = () => deps.chrome.openTab() === "workflows";
  const setWorkflowsOpen = (open: boolean) => {
    if (open) deps.chrome.selectTab("workflows");
    else if (deps.chrome.openTab() === "workflows") deps.chrome.selectTab(null);
  };

  createEffect(on(
    [deps.sessionId, workflowsOpen, deps.offline, deps.projectDir, () => deps.projects],
    ([sessionId, open, offline, projectDir, projects]) => {
      if (!sessionId || !open || offline) return;
      const client = getLycaonClient();
      if (!client) return;
      void refreshWorkflowState(
        deps.appStore,
        client,
        sessionId,
        projectDir,
        projects,
      ).catch(() => undefined);
    },
  ));

  return { workflowsOpen, setWorkflowsOpen };
}
