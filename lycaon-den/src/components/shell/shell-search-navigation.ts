import { shellSearchTargets } from "./shell-search-targets.ts";
import { type ShellScope } from "./shell-scope.ts";
import { createMemo } from "solid-js";
import { type ContextNavItemId } from "../../../shared/app-state-types.ts";
import { noticeReporterFor } from "../../platform/connection/app-connection.ts";
import { type CrossbarGotoTarget } from "../../search/crossbar-model.ts";
import { isContextNavItemId } from "../../settings/editor/context-nav-prefs.ts";
import { findSettingDefinition } from "../../settings/settings-registry.ts";
import { requestSettingReveal } from "../../settings/settings-reveal.ts";
import { type OpenWorkerOptions } from "../../chat/worker/workers-model.ts";
import { revealChicklet } from "../../chat/transcript/presentation/transcript-reveal.ts";
import { clientNoticeError } from "../../notices/client-notices.ts";
import { sessionScope } from "../../notices/notice-scope.ts";
import { type SearchNavTarget } from "../../search/search-hit-nav.ts";
import { requestOpenWorklog } from "../../search/search-nav.ts";
import { stageSwitchKind } from "./session-creation.ts";
import { type SessionNavigation } from "./session-navigation.ts";
import { type ShellSearchState } from "./shell-search-state.ts";
import { type ShellNavigation } from "./shell-navigation-state.ts";
import { type AttentionStore } from "../../store/attention-store.ts";

type SearchNavigationDependencies = Pick<ShellScope, "shell" | "recents" | "showProjects" | "projects" | "activeProject" | "setNav">
  & Pick<ShellSearchState, "closeSearch"> & Pick<SessionNavigation, "openProject" | "resumeSession"> & {
  closeLauncher: () => void;
  attentionIndex: () => ReturnType<AttentionStore["index"]>;
  securityScannersFeatureOn: () => boolean;
  routeToStage: ShellNavigation["routeToStage"];
  selectSettingsSection: ShellNavigation["selectSettingsSection"];
  openSettings: ShellNavigation["openSettings"];
  openStageWithPlacement: (stage: ContextNavItemId, arrival: "routed" | "deliberate") => void;
  openConfiguration: () => void;
  goHome: () => void;
  openWorkers: (workerId?: string, options?: OpenWorkerOptions) => void;
};

export type ShellSearchNavigation = ReturnType<typeof createShellSearchNavigation>;

export function createShellSearchNavigation({ shell, recents, closeSearch, closeLauncher,
  showProjects, openProject, resumeSession, openWorkers, projects, activeProject, setNav,
  attentionIndex, securityScannersFeatureOn, routeToStage, selectSettingsSection, openSettings,
  openStageWithPlacement, openConfiguration, goHome }: SearchNavigationDependencies) {
  const crossbarGotoTargets = createMemo(() => shellSearchTargets(
    projects.state.projects, recents.state.recents,
    attentionIndex(), activeProject(), securityScannersFeatureOn,
  ));

  /** Opens a setting's section and tab; Settings reveals the row once it renders. */
  const openSettingFromGoto = (settingId: string) => {
    const setting = findSettingDefinition(settingId);
    if (!setting) return;
    routeToStage("settings", () => {
      selectSettingsSection(setting.section, {
        general: setting.section === "general" ? setting.tab : undefined,
        advanced: setting.section === "debug" ? setting.tab : undefined,
      });
      requestSettingReveal(setting);
      setNav("settings");
    });
  };

  const navigateFromCrossbarGoto = (target: CrossbarGotoTarget) => {
    if (target.kind === "session") {
      void navigateFromSearch({
        projectId: target.projectId,
        sessionId: target.sessionId,
      });
      return;
    }
    if (target.kind === "project") {
      openProject(target.projectId);
      return;
    }
    if (target.kind === "setting") {
      openSettingFromGoto(target.settingId);
      return;
    }
    // Go-to jumps retain their routed return target.
    if (isContextNavItemId(target.surface)) {
      openStageWithPlacement(target.surface, "routed");
      return;
    }
    switch (target.surface) {
      case "context":
        routeToStage("context", openConfiguration);
        return;
      case "chats":
        setNav("chats");
        return;
      case "home":
        goHome();
        return;
      case "settings":
        openSettings();
        return;
      default:
        target.surface satisfies never;
        return;
    }
  };

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

  return { navigateFromSearch, revealFromSearch, crossbarGotoTargets, navigateFromCrossbarGoto };
}
