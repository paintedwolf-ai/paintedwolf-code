import { type AttentionStore } from "../../store/attention-store.ts";
import { type NoticeStore } from "../../notices/notice-store.ts";
import { type SettingsStore } from "../../store/settings-store.ts";
import { type CostStore } from "../../store/cost-store.ts";
import { type ContextNavItemId } from "../../../shared/app-state-types.ts";
import { type Project } from "../../api/types.ts";
import { type ActiveChat, type deriveStageScope } from "../../shell/stage-scope.ts";
import { type AppStore } from "../../store/app-state-model.ts";
import { type ProjectSessionsStore } from "../../store/project-sessions-store.ts";
import { type createProjectsStore } from "../../store/projects-store.ts";
import { type createRecentsStore } from "../../store/recents-store.ts";
import { type createShellStore } from "../../store/shell-store.ts";
import { type PeerViewSwitcherHandle } from "./PeerViewSwitcher.tsx";
import { type RecentChatSwitcherHandle } from "./RecentChatSwitcher.tsx";
import { type Nav } from "./shell-navigation-state.ts";

/** What the Shell holds and hands to the controllers beside it. Each controller
 * declares the members it reads as a `Pick` of this scope. */
export type ShellScope = {
  shell: ReturnType<typeof createShellStore>;
  appStore: AppStore;
  projects: ReturnType<typeof createProjectsStore>;
  recents: ReturnType<typeof createRecentsStore>;
  /** Backend-truth chat inventory for the foreground project. */
  projectSessions: ProjectSessionsStore;
  activeProjectId: () => string | null;
  activeProject: () => Project | undefined;
  /** The foreground chat, as soon as the shell selects it. */
  activeChat: () => ActiveChat | null;
  /** The foreground chat once its stage scope is ready; stable across scope updates. */
  readyChat: () => ActiveChat | null;
  stageScope: () => ReturnType<typeof deriveStageScope>;
  nav: () => Nav;
  setNav: (nav: Nav) => void;
  showProjects: () => void;
  stageAvailable: (stage: ContextNavItemId) => boolean;
  /** The stage column resolves to a split beside the conversation. */
  splitLive: () => boolean;
  /** Reports a shell condition that names no project or chat. */
  reportError: (error: unknown) => void;
  /** This window presents one peer subject rather than the main workspace. */
  isPeerWindow: boolean;
  // Overlays that shell commands raise and Escape dismisses.
  helpOpen: () => boolean;
  setHelpOpen: (open: boolean) => void;
  launcherOpen: () => boolean;
  setLauncherOpen: (open: boolean) => void;
  recentSwitcher: () => RecentChatSwitcherHandle | undefined;
  peerViewSwitcher: () => PeerViewSwitcherHandle | undefined;
};

type ProjectsStore = ReturnType<typeof createProjectsStore>;
type RecentsStore = ReturnType<typeof createRecentsStore>;
type ShellStore = ReturnType<typeof createShellStore>;

export type ShellProps = {
  shell: ShellStore;
  recents: RecentsStore;
  projects: ProjectsStore;
  /** Attention across all projects. */
  attention: AttentionStore;
  /** Host chat inventory for the active project. */
  projectSessions: ProjectSessionsStore;
  appStore: AppStore;
  /** Scope-keyed notice state. */
  notices: NoticeStore;
  settingsStore: SettingsStore;
  costStore: CostStore;
};
