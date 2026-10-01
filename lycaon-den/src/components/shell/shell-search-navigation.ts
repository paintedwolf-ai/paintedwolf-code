import type { OpenWorkerOptions } from "../../chat/worker/workers-model.ts";
import { revealChicklet } from "../../chat/transcript/presentation/transcript-reveal.ts";
import { clientNoticeError } from "../../notices/client-notices.ts";
import { sessionScope } from "../../notices/notice-scope.ts";
import { noticeReporterFor } from "../../platform/connection/app-connection.ts";
import type { SearchNavTarget } from "../../search/search-hit-nav.ts";
import { requestOpenWorklog } from "../../search/search-nav.ts";
import { stageSwitchKind } from "./session-creation.ts";
import type { SessionNavigation } from "./session-navigation.ts";
import type { ShellScope } from "./shell-scope.ts";
import type { ShellSearchState } from "./shell-search-state.ts";

type SearchNavigationDependencies = Pick<ShellScope, "shell" | "recents" | "showProjects">
  & Pick<ShellSearchState, "closeSearch"> & Pick<SessionNavigation, "openProject" | "resumeSession"> & {
  closeLauncher: () => void;
  openWorkers: (workerId?: string, options?: OpenWorkerOptions) => void;
};

export type ShellSearchNavigation = ReturnType<typeof createShellSearchNavigation>;

export function createShellSearchNavigation({ shell, recents, closeSearch, closeLauncher,
  showProjects, openProject, resumeSession, openWorkers }: SearchNavigationDependencies) {
  const navigateFromSearch = async (target: SearchNavTarget) => {
    closeSearch();
    if (!target.sessionId) {
      openProject(target.projectId);
      return;
    }
    const kind = stageSwitchKind(shell, target.projectId);
    shell.beginStageSwitch({
      projectId: target.projectId,
      kind,
    });
    closeLauncher();
    showProjects();
    const row = recents.state.recents.find(
      (r) =>
        r.projectId === target.projectId && r.sessionId === target.sessionId,
    );
    if (row) {
      await resumeSession(row, { kind, stageSwitchDone: true });
    } else {
      await resumeSession(
        {
          projectId: target.projectId,
          sessionId: target.sessionId,
          title: "",
        },
        { kind, stageSwitchDone: true },
      );
    }
    if (target.openWorklog) requestOpenWorklog();
    const reveal = target.reveal;
    const revealSessionId = target.sessionId;
    if (reveal && revealSessionId) {
      queueMicrotask(() =>
        revealFromSearch(reveal, target.projectId, revealSessionId),
      );
    }
  };

  const revealFromSearch = (
    reveal: NonNullable<SearchNavTarget["reveal"]>,
    projectId: string,
    sessionId: string,
  ) => {
    if (reveal.worker) {
      openWorkers(reveal.worker.workerId, { scrollTo: "evidence" });
      return;
    }
    void revealChicklet(
      () => document.querySelector<HTMLElement>(".den-chat-stream"),
      { sessionId, anchor: reveal },
      { timeoutMs: 6000 },
    ).then((revealed) => {
      // The chat is open either way; only the row is missing.
      if (revealed) return;
      noticeReporterFor(sessionScope(projectId, sessionId)).reportError(
        clientNoticeError("transcript_row_gone"),
      );
    });
  };

  return { navigateFromSearch, revealFromSearch };
}
