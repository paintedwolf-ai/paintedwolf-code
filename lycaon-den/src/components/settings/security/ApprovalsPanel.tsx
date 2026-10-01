import { For, Show, createSignal } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type {
  ApprovalPosture,
  ManagedApprovalRule,
  UpdateApprovalsSettingsRequest,
} from "../../../api/types.ts";
import type { SettingsStore } from "../../../store/settings-store.ts";
import {
  DEFAULT_AI_RATIONALE_ENABLED,
  DEFAULT_APPROVAL_POSTURE,
  APPROVALS_SETTINGS_COPY,
  APPROVAL_POSTURE_CARDS,
  approvalsFollowingSummary,
  postureLabel,
  type ApprovalsSettingsTab,
} from "../../../settings/security/approvals-settings-copy.ts";
import { PROJECT_SETTINGS_OVERLAY_COPY } from "../../../settings/security/project-settings-overlay-copy.ts";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenRadioControl } from "../../primitives/DenRadio.tsx";
import { ApprovalsIcon, SettingsEditorTitle } from "../SettingsEditorTitle.tsx";
import { ProjectSettingsOverrideControl } from "../ProjectSettingsOverrideControl.tsx";
import { SettingsScopeLede } from "../SettingsScopeLede.tsx";
import { SavedApprovalsPanel } from "./SavedApprovalsPanel.tsx";
import { DetectionsPanel } from "./DetectionsPanel.tsx";
import { UnderlineTabs } from "../UnderlineTabs.tsx";
import { followSettingRevealTab } from "../../../settings/settings-reveal.ts";
import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";

type Props = {
  client: LycaonClient;
  settingsStore: SettingsStore;
  /** Optional initial tab — defaults to When we ask. Global only. */
  initialTab?: ApprovalsSettingsTab;
  /** Project Configuration overlay (always project scope). */
  projectId?: string;
  sessionId?: string;
  alwaysProjectScope?: boolean;
  /** Jump to the counterpart surface (Settings ↔ project). */
  counterpartLabel?: string;
  onOpenCounterpart?: () => void;
};

const TABS: { id: ApprovalsSettingsTab; label: string }[] = [
  { id: "ask", label: APPROVALS_SETTINGS_COPY.tabAsk },
  { id: "saved", label: APPROVALS_SETTINGS_COPY.tabSaved },
  { id: "detections", label: APPROVALS_SETTINGS_COPY.tabDetections },
];


/** Local service grants are created only from device settings. */
export function ApprovalsPanel(props: Props) {
  const [tab, setTab] = createSignal<ApprovalsSettingsTab>(
    props.initialTab ?? (props.alwaysProjectScope ? "saved" : "ask"),
  );
  const [busy, setBusy] = createSignal(false);

  const projectLocal = () => props.alwaysProjectScope === true;
  followSettingRevealTab("approvals", (next) => {
    if (!projectLocal()) setTab(next);
  });

  const putPatch = async (patch: UpdateApprovalsSettingsRequest) => {
    return props.client.updateApprovalsSettings(patch, projectLocal() ? props.projectId : undefined);
  };

  const applyPatch = async (patch: UpdateApprovalsSettingsRequest) => {
    setBusy(true);
    props.settingsStore.actions.setError(undefined);
    try {
      const updated = await putPatch(patch);
      props.settingsStore.actions.setApprovals(updated);
    } catch (err) {
      props.settingsStore.actions.setError(
        err instanceof Error ? err.message : APPROVALS_SETTINGS_COPY.saveError,
      );
    } finally {
      setBusy(false);
    }
  };

  const projectOverrideEnabled = (): boolean => {
    const sources = props.settingsStore.state.approvals?.field_sources;
    if (!sources) return false;
    return (
      sources.approval_posture === "override" ||
      sources.ai_rationale_enabled === "override" ||
      sources.never_ask === "override"
    );
  };

  const posture = (): ApprovalPosture =>
    props.settingsStore.state.approvals?.approval_posture ??
    DEFAULT_APPROVAL_POSTURE;

  const neverAsk = (): boolean =>
    props.settingsStore.state.approvals?.never_ask ?? false;

  const devicePosture = (): ApprovalPosture =>
    props.settingsStore.state.approvals?.defaults?.approval_posture ?? posture();

  const deviceNeverAsk = (): boolean =>
    props.settingsStore.state.approvals?.defaults?.never_ask ?? neverAsk();

  const availablePostureCards = () => {
    if (!projectLocal()) return APPROVAL_POSTURE_CARDS;
    const minimum = APPROVAL_POSTURE_CARDS.findIndex(
      (card) => card.id === devicePosture(),
    );
    return APPROVAL_POSTURE_CARDS.slice(Math.max(0, minimum));
  };

  const applyPosture = async (next: ApprovalPosture) => {
    if (posture() === next) return;
    await applyPatch({ approval_posture: next });
  };

  const aiRationaleEnabled = (): boolean =>
    props.settingsStore.state.approvals?.ai_rationale_enabled ??
    DEFAULT_AI_RATIONALE_ENABLED;

  const applyAIRationale = async (next: boolean) => {
    if (aiRationaleEnabled() === next) return;
    await applyPatch({ ai_rationale_enabled: next });
  };

  const followingSummary = (): string => {
    const defaults = props.settingsStore.state.approvals?.defaults;
    return approvalsFollowingSummary({
      posture: defaults?.approval_posture ?? posture(),
      aiRationaleEnabled:
        defaults?.ai_rationale_enabled ?? aiRationaleEnabled(),
      neverAsk: defaults?.never_ask ?? neverAsk(),
    });
  };

  const applyProjectOverride = async (enabled: boolean) => {
    if (enabled === projectOverrideEnabled()) return;
    if (!enabled) {
      await applyPatch({
        approval_posture: null,
        ai_rationale_enabled: null,
        never_ask: null,
      });
      return;
    }
    // Seed overrides from the effective device values.
    await applyPatch({
      approval_posture: posture(),
      ai_rationale_enabled: aiRationaleEnabled(),
      ...(deviceNeverAsk() ? { never_ask: false } : {}),
    });
  };

  const restoreProjectApprovals = async () => {
    await applyPatch({ never_ask: false });
  };

  const managedRules = (): ManagedApprovalRule[] =>
    props.settingsStore.state.approvals?.managed_rules ?? [];

  const managedRulesSection = () => (
    <section
      class="den-approvals-block"
      data-testid="approval-managed-rules"
      {...settingAnchor("extension-policies")}
    >
      <h3 class="den-settings-overline">
        {settingLabel("extension-policies")}
      </h3>
      <p class="den-settings-hint">
        {projectLocal()
          ? APPROVALS_SETTINGS_COPY.managedRulesProjectIntro
          : APPROVALS_SETTINGS_COPY.managedRulesIntro}
      </p>
      <Show
        when={managedRules().length > 0}
        fallback={
          <p class="den-settings-hint" data-testid="approval-managed-rules-empty">
            {APPROVALS_SETTINGS_COPY.managedRulesEmpty}
          </p>
        }
      >
        <ul class="den-approval-policy-list">
          <For each={managedRules()}>
            {(rule) => (
              <li
                class="den-approval-policy-rule"
                data-testid={`approval-managed-rule-${rule.unit_id}`}
              >
                <div class="den-approval-policy-rule__head">
                  <code>{rule.pattern}</code>
                  <span class="den-settings-badge" data-effect={rule.effect}>
                    {APPROVALS_SETTINGS_COPY.managedRuleEffect(rule.effect)}
                  </span>
                  <span class="den-settings-badge">
                    {APPROVALS_SETTINGS_COPY.managedRuleScope(rule.scope)}
                  </span>
                </div>
                <p class="den-settings-hint">
                  {rule.category} · {rule.pack_id} · {rule.unit_id}
                </p>
              </li>
            )}
          </For>
        </ul>
      </Show>
    </section>
  );

  const askEditors = () => (
    <>
      <p class="den-settings-hint">{APPROVALS_SETTINGS_COPY.askIntro}</p>

      <Show when={neverAsk()}>
        <aside class="den-approvals-explain" role="status" data-testid="approvals-off-notice">
          <p class="den-settings-warn">
            {projectLocal()
              ? APPROVALS_SETTINGS_COPY.projectOffNotice
              : APPROVALS_SETTINGS_COPY.offNotice(postureLabel(posture()))}
          </p>
          <Show when={projectLocal()}>
            <DenButton
              variant="primary"
              compact
              disabled={busy()}
              data-testid="approvals-project-restore"
              onClick={() => void restoreProjectApprovals()}
            >
              {APPROVALS_SETTINGS_COPY.projectRestore}
            </DenButton>
          </Show>
        </aside>
      </Show>

      <section class="den-approvals-block" {...settingAnchor("approval-level")}>
        <h3 class="den-settings-overline">
          {settingLabel("approval-level")}
        </h3>
        <fieldset class="den-approvals-posture-cards" disabled={busy()}>
          <For each={availablePostureCards()}>
            {(card) => (
              <label
                class="den-approvals-posture-card"
                data-testid={`approval-posture-${card.id}`}
              >
                <span class="den-approvals-posture-card-head">
                  <DenRadioControl
                    name="approval-posture"
                    checked={posture() === card.id}
                    onChange={() => void applyPosture(card.id)}
                  />
                  <strong>{card.label}</strong>
                </span>
                <span class="den-settings-hint">{card.tagline}</span>
              </label>
            )}
          </For>
        </fieldset>
      </section>

      <section class="den-approvals-block">
        <h3 class="den-settings-overline">
          {APPROVALS_SETTINGS_COPY.optionsHeading}
        </h3>
        <div class="den-settings-pref-group">
          <div
            class="den-settings-pref-row"
            data-testid="approval-ai-rationale-setting"
            {...settingAnchor("approval-ai-note")}
          >
            <div class="den-settings-pref-copy">
              <span class="den-settings-pref-label">
                {settingLabel("approval-ai-note")}
              </span>
              <p class="den-settings-hint">
                {APPROVALS_SETTINGS_COPY.aiRationaleHint}
              </p>
            </div>
            <DenCheckbox
              checked={aiRationaleEnabled()}
              disabled={busy()}
              data-testid="approval-ai-rationale-toggle"
              onChange={(e) => void applyAIRationale(e.currentTarget.checked)}
            >
              <span class="sr-only">
                {settingLabel("approval-ai-note")}
              </span>
            </DenCheckbox>
          </div>
        </div>
      </section>

      <section class="den-approvals-block" data-testid="approval-baseline">
        <h3 class="den-settings-overline">
          {APPROVALS_SETTINGS_COPY.baselineHeading}
        </h3>
        <div class="den-approvals-baseline-card">
          <For each={APPROVALS_SETTINGS_COPY.baselineLines}>
            {(line) => <p class="den-settings-hint">{line}</p>}
          </For>
        </div>
      </section>
    </>
  );

  return (
    <div
      class="den-settings-editor"
      classList={{ "den-settings-editor--project": projectLocal() }}
      data-testid="approvals-settings"
      data-scope={projectLocal() ? "project" : "global"}
    >
      <SettingsEditorTitle
        icon={<ApprovalsIcon />}
        scopeBadge={
          projectLocal() ? PROJECT_SETTINGS_OVERLAY_COPY.badge : undefined
        }
      >
        {APPROVALS_SETTINGS_COPY.title}
      </SettingsEditorTitle>

      <Show when={!projectLocal() || tab() === "ask"}>
        <SettingsScopeLede
          scope={projectLocal() ? "project" : "device"}
          counterpartLabel={props.counterpartLabel}
          onOpenCounterpart={props.onOpenCounterpart}
        >
          <p class="den-settings-hint">{APPROVALS_SETTINGS_COPY.intro}</p>
        </SettingsScopeLede>
      </Show>

        <UnderlineTabs aria-label={APPROVALS_SETTINGS_COPY.tabsAriaLabel}>
          <For each={projectLocal() ? TABS.filter((item) => item.id !== "detections") : TABS}>
            {(t) => (
              <button
                type="button"
                role="tab"
                id={`approvals-tab-${t.id}`}
                aria-selected={tab() === t.id}
                aria-controls={`approvals-panel-${t.id}`}
                class="den-underline-tab"
                data-active={tab() === t.id}
                data-testid={`approvals-tab-${t.id}`}
                onClick={() => setTab(t.id)}
              >
                {t.label}
              </button>
            )}
          </For>
        </UnderlineTabs>

        <div
          id="approvals-panel-ask"
          role="tabpanel"
          aria-labelledby="approvals-tab-ask"
          hidden={tab() !== "ask"}
          class="den-approvals-ask"
          data-testid="approvals-panel-ask"
        >
          <Show when={projectLocal()}
            fallback={askEditors()}
          >
            <ProjectSettingsOverrideControl
              settingsPath={PROJECT_SETTINGS_OVERLAY_COPY.settingsPath.approvals}
              enabled={projectOverrideEnabled()}
              disabled={busy()}
              followingSummary={followingSummary()}
              onChange={(enabled) => void applyProjectOverride(enabled)}
            />
            <Show when={projectOverrideEnabled()}>
              {askEditors()}
            </Show>
          </Show>
          {managedRulesSection()}
        </div>

        <div
          id="approvals-panel-saved"
          role="tabpanel"
          aria-labelledby="approvals-tab-saved"
          hidden={tab() !== "saved"}
          data-testid="approvals-panel-saved"
        >
          <Show when={tab() === "saved"}>
            <SavedApprovalsPanel client={props.client} projectId={projectLocal() ? props.projectId : undefined} sessionId={projectLocal() ? props.sessionId : undefined} />
          </Show>
        </div>

        <Show when={!projectLocal()}><div
          id="approvals-panel-detections"
          role="tabpanel"
          aria-labelledby="approvals-tab-detections"
          hidden={tab() !== "detections"}
          data-testid="approvals-panel-detections"
        >
          <Show when={tab() === "detections"}>
            <DetectionsPanel client={props.client} />
          </Show>
        </div></Show>
    </div>
  );
}
