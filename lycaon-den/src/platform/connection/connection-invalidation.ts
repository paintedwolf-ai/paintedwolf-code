import type { LycaonClient } from "../../api/client.ts";
import type { SettingsArea } from "../../api/types.ts";
import type { Project } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { SettingsStore } from "../../store/settings-store.ts";
import { refreshPreflight } from "../persistence/preflight-store.ts";
import { invalidateContributionFrame } from "../../contributions/contribution-store.ts";
import type { StoreInvalidation } from "../../api/events.ts";
import { createSettingsInvalidationScheduler, type SettingsInvalidationScheduler } from "../../settings/settings-invalidation.ts";
import type { SettingsInvalidationSlice } from "../../settings/settings-actions.ts";
import { refreshFileSummariesSetting } from "../../settings/editor/file-summary-settings.ts";
import { invalidateApprovalGrantsCache } from "../../settings/security/approval-grants-cache.ts";
import { refreshModelPolicy, refreshProviders, refreshProviderKinds, refreshPricing } from "../../settings/settings-actions.ts";
import { costTrackingEnabled } from "../../cost/cost-tracking.ts";
import type { CostStore } from "../../store/cost-store.ts";
import { refreshWorkflowState } from "../../chat/workflow/workflow-actions.ts";
import { refreshProgress } from "../../chat/progress/progress-actions.ts";
import { createSessionInvalidationSchedulers } from "../../chat/session/session-invalidation.ts";
import { refreshCodeScanCache } from "../../chat/session/session-reconcile.ts";
import type { EventSubscriptionOptions } from "../../api/events.ts";
import type { EventScope } from "../../api/types.ts";
type SettingsAreaEffect = {
  slices?: readonly SettingsInvalidationSlice[];
  apply?: (appStore: AppStore, client: LycaonClient) => void;
};

const SETTINGS_AREA_EFFECTS: Readonly<Record<SettingsArea, SettingsAreaEffect>> = {
  approvals: {
    slices: ["approvals"],
    apply: (appStore) => {
      invalidateApprovalGrantsCache();
      appStore.actions.bumpApprovalsRevision();
    },
  },
  extensions: {
    apply: (appStore) => {
      appStore.actions.bumpExtensionsRevision();
      void invalidateContributionFrame();
    },
  },
  file_summaries: {
    apply: (_appStore, client) => void refreshFileSummariesSetting(client).catch(() => undefined),
  },
  limits: { slices: ["limits"] },
  mcp: { apply: () => void invalidateContributionFrame() },
  power: {},
  pricing: { slices: ["pricing"] },
  project_trust: { apply: (appStore) => appStore.actions.bumpProjectTrustRevision() },
  review: { slices: ["review"] },
  security_scanners: {},
  verify: { apply: (appStore) => appStore.actions.bumpVerifyDetectRevision() },
  web_research: {},
};

type InvalidationPorts = {
  client(): LycaonClient | null;
  projects(): readonly Project[];
  settings(): SettingsStore | null;
  costs(): CostStore | null;
};
export class ConnectionInvalidation {
  private settingsInvalidation: SettingsInvalidationScheduler | null = null;
  private sessionInvalidation: ReturnType<typeof createSessionInvalidationSchedulers> | null = null;
  constructor(private readonly ports: InvalidationPorts) {}
  resetSettings(): void {
    this.settingsInvalidation?.cancel();
    this.settingsInvalidation = null;
  }
  cancel(): void {
    this.resetSettings();
    this.sessionInvalidation?.cancel();
    this.sessionInvalidation = null;
  }
  bind(appStore: AppStore): void {
    this.cancel();
    this.sessionInvalidation = createSessionInvalidationSchedulers(
      appStore,
      () => this.ports.client(),
      this.ports.projects,
      () => (this.ports.costs()?.state.liveConsumers ?? 0) > 0,
    );

    const settings = this.ports.settings();
    if (settings) {
      this.settingsInvalidation = createSettingsInvalidationScheduler(
        settings,
        this.ports.client,
        this.ports.projects,
        () => appStore.state.currentSession?.workspace_path,
      );
    }

  }
  settingsEvent(appStore: AppStore, event: Parameters<NonNullable<EventSubscriptionOptions["onSettingsEvent"]>>[0]): void {
    const currentClient = this.ports.client();
    if (!currentClient) return;
    const effect = SETTINGS_AREA_EFFECTS[event.area];
    if (effect.slices) this.settingsInvalidation?.schedule(effect.slices);
    effect.apply?.(appStore, currentClient);
  }
  invalidate(appStore: AppStore, keys: readonly StoreInvalidation[], scope: EventScope): void {
    const sessionId = appStore.state.currentSession?.id;
    const projectDir = appStore.state.currentSession?.workspace_path;
    const currentClient = this.ports.client();
    if (!currentClient) return;
    if (
      this.settingsInvalidation &&
      (keys.includes("providers") || keys.includes("model_policy"))
    ) {
      const slices: SettingsInvalidationSlice[] = [];
      if (keys.includes("providers")) slices.push("providers");
      if (keys.includes("model_policy")) slices.push("model_policy");
      this.settingsInvalidation.schedule(slices);
    }
    // Readiness invalidations refresh one coherent report.
    if (keys.includes("providers") || keys.includes("readiness")) {
      void refreshPreflight();
    }
    if (keys.includes("providers") || keys.includes("model_policy")) {
      appStore.actions.bumpModelPolicyRevision();
    }
    if (scope.kind === "session" && scope.session_id !== sessionId) return;
    if (!projectDir) return;
    if (keys.includes("workers") && sessionId) {
      this.sessionInvalidation?.scheduleWorkers(projectDir, sessionId);
    }
    if (keys.includes("workflows") && sessionId) {
      const client = currentClient;
      this.sessionInvalidation?.scheduleWorkflow(() =>
        refreshWorkflowState(
          appStore,
          client,
          sessionId,
          projectDir,
          this.ports.projects(),
        ).catch(() => undefined),
      );
    }
    if (keys.includes("session") && sessionId) {
      // Busy and idle transitions refresh the progress clock.
      const client = currentClient;
      this.sessionInvalidation?.scheduleProgress(async () => {
        await refreshProgress(appStore, client, sessionId).catch(
          () => undefined,
        );
      });
    }
    if (keys.includes("board") && sessionId) {
      this.sessionInvalidation?.scheduleBoard(
        projectDir,
        sessionId,
        keys.includes("cost"),
      );
    }
    const currentCosts = this.ports.costs();
    const currentSettings = this.ports.settings();
    if (
      keys.includes("cost") &&
      currentCosts &&
      currentSettings &&
      costTrackingEnabled(currentSettings)
    ) {
      const client = currentClient;
      const costStore = currentCosts;
      const costSessionId = sessionId;
      this.sessionInvalidation?.scheduleCost(async () => {
        if (costSessionId) {
          await costStore.refreshSession(client, costSessionId).catch(
            () => undefined,
          );
        }
        // Self-fetching surfaces refetch here.
        costStore.emitInvalidated();
      });
    }
    if (keys.includes("scan")) {
      void refreshCodeScanCache(appStore, currentClient).catch(
        () => undefined,
      );
    }
  }
  async hydrate(client: LycaonClient, shouldApply: () => boolean): Promise<void> {
    const settings = this.ports.settings();
    if (!settings) return;
    try {
      await Promise.all([
        refreshProviders(settings, client, shouldApply),
        refreshProviderKinds(settings, client, shouldApply),
        refreshModelPolicy(settings, client, this.ports.projects(), shouldApply),
        refreshPricing(settings, client, shouldApply),
      ]);
    } catch { /* Settings hydration is best-effort. */ }
  }
}
