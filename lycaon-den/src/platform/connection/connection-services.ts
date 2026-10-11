import type { AppStore } from "../../store/app-state-model.ts";
import type { SettingsStore } from "../../store/settings-store.ts";
import type { RecentsStore } from "../../store/recents-store.ts";
import type { ShellStore } from "../../store/shell-store.ts";
import type { ProjectsStore } from "../../store/projects-store.ts";
import type { NoticeStore } from "../../notices/notice-store.ts";
import type { EntityRetire } from "../../lifecycle/entity-retire.ts";
import type { CostStore } from "../../store/cost-store.ts";

/** Services installed together for one window connection lifetime. */
export type ConnectionServices = Readonly<{
  app: AppStore;
  settings: SettingsStore;
  recents: RecentsStore;
  shell: ShellStore;
  projects: ProjectsStore;
  notices: NoticeStore;
  retire: EntityRetire;
  costs: CostStore;
}>;
