import { createAppStore } from "../store/app-state.ts";
import { createSettingsStore } from "../store/settings-store.ts";
import { createCostStore } from "../store/cost-store.ts";
import { createRecentsStore } from "../store/recents-store.ts";
import { createShellStore } from "../store/shell-store.ts";
import { createProjectsStore } from "../store/projects-store.ts";
import { createNoticeStore } from "../notices/notice-store.ts";
import { createAttentionStore } from "../store/attention-store.ts";
import { createEntityRetire } from "../lifecycle/entity-retire.ts";
import type { ConnectionServices } from "../platform/connection/connection-services.ts";

export function connectionServicesFixture(overrides: Partial<ConnectionServices> = {}): ConnectionServices {
  const notices = overrides.notices ?? createNoticeStore();
  const recents = overrides.recents ?? createRecentsStore();
  return {
    app: createAppStore(), settings: createSettingsStore(), costs: createCostStore(),
    shell: createShellStore("connected"), projects: createProjectsStore(() => null, { onProjectEvent: () => () => {} }),
    notices, recents,
    retire: createEntityRetire({ notices, recents, attention: createAttentionStore(() => null, { onAttentionEvent: () => () => {} }) }),
    ...overrides,
  };
}
