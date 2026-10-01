import { createSettingsDraft } from "../../settings/settings-draft.ts";
import { Show, createEffect, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ModelPolicy, ModelPolicyPatch } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { SettingsStore } from "../../store/settings-store.ts";
import {
  assignmentPatch,
  cloneModelPolicy,
  modelPoliciesEqual,
  policyHasModelAssignments,
} from "../../settings/providers/models-editor-model.ts";
import { ShowLatest } from "../primitives/ShowLatest.tsx";
import { SettingsEditorTitle, ProvidersIcon } from "../settings/SettingsEditorTitle.tsx";
import { ProjectSettingsOverrideControl } from "../settings/ProjectSettingsOverrideControl.tsx";
import { SettingsScopeLede } from "../settings/SettingsScopeLede.tsx";
import { PROJECT_SETTINGS_OVERLAY_COPY } from "../../settings/security/project-settings-overlay-copy.ts";
import { MODELS_SETTINGS_COPY } from "../../settings/providers/models-settings-copy.ts";
import { AI_PROVIDERS_SECTION_LABEL } from "../../settings/settings-nav-model.ts";
import { createSettingsAutoSave } from "../../settings/settings-auto-save.ts";
import { resolveProjectSettingsScope } from "../../settings/settings-scope.ts";
import type { createProjectsStore } from "../../store/projects-store.ts";
import { ModelPolicyPanel } from "./ModelPolicyPanel.tsx";
import { ThinkingOverridesPanel } from "./ThinkingOverridesPanel.tsx";

type ProjectsStore = ReturnType<typeof createProjectsStore>;

type Props = {
  client: LycaonClient;
  appStore: AppStore;
  settingsStore: SettingsStore;
  projects: ProjectsStore;
  projectDir?: string;
  counterpartLabel?: string;
  onOpenCounterpart?: () => void;
};

const CLEAR_PROJECT_ASSIGNMENTS: ModelPolicyPatch = {
  coordinator: null,
  lite: null,
  agent_pool: { selection: "round_robin", models: [] },
};

function modelPolicySummary(policy: ModelPolicy | undefined): string {
  if (!policy) return "model policy";
  const coord = policy.coordinator;
  if (coord?.provider_id && coord.model) {
    return `${coord.provider_id} / ${coord.model}`;
  }
  return "model policy";
}

/** Project configuration → AI providers (override-gated model policy). */
export function ModelsEditor(props: Props) {
  const draftState = createSettingsDraft<ModelPolicy | null>(null);
  const draft = draftState.value;
  const setDraft = draftState.set;
  const [busyOverride, setBusyOverride] = createSignal(false);
  const [settingsSummary, setSettingsSummary] = createSignal("model policy");
  const [inheritedPolicy, setInheritedPolicy] = createSignal<ModelPolicy>();
  const [inheritanceError, setInheritanceError] = createSignal<string>();

  const providers = () => props.settingsStore.state.providers;
  const hasStoredAssignments = () => {
    const policy = props.settingsStore.state.modelPolicy;
    return policy ? policyHasModelAssignments(policy) : false;
  };
  const projectOverrideEnabled = () => hasStoredAssignments();
  const projectScope = () =>
    resolveProjectSettingsScope(props.projectDir, props.projects.state.projects);

  createEffect(() => {
    props.client;
    props.settingsStore.state.modelPolicy;
    void props.client
      .getModelPolicySettings(undefined, true)
      .then((policy) => { setSettingsSummary(modelPolicySummary(policy)); setInheritedPolicy(policy); setInheritanceError(undefined); })
      .catch(() => { setInheritedPolicy(undefined); setInheritanceError("Device thinking settings could not be loaded. Reopen this panel to try again."); });
  });

  const autoSave = createSettingsAutoSave({
    isReady: () => draft() != null && projectOverrideEnabled(),
    isDirty: () => {
      const policy = draft();
      const stored = props.settingsStore.state.modelPolicy;
      if (!policy || !stored) return false;
      return !modelPoliciesEqual({ ...policy, thinking_overrides: [] }, { ...stored, thinking_overrides: [] });
    },
    save: async () => {
      const policy = draft();
      if (!policy) return;
      props.settingsStore.actions.setError(undefined);
      try {
        const { projectId } = projectScope();
        const updated = await props.client.updateModelPolicySettings(assignmentPatch(policy), projectId);
        props.settingsStore.actions.setModelPolicy(updated);
      } catch (err) {
        props.settingsStore.actions.setError(
          err instanceof Error ? err.message : String(err),
        );
      }
    },
  });

  createEffect(() => {
    const policy = props.settingsStore.state.modelPolicy;
    if (!policy) return;
    draftState.receive(cloneModelPolicy(policy), autoSave.runSynced);
  });

  createEffect(() => {
    draft();
    autoSave.schedule();
  });

  const applyProjectOverride = async (enabled: boolean) => {
    if (enabled === projectOverrideEnabled()) return;
    setBusyOverride(true);
    props.settingsStore.actions.setError(undefined);
    try {
      const { projectId } = projectScope();
      if (!enabled) {
        const updated = await props.client.updateModelPolicySettings(CLEAR_PROJECT_ASSIGNMENTS, projectId);
        props.settingsStore.actions.setModelPolicy(updated);
        return;
      }
      const global = await props.client.getModelPolicySettings();
      const next: ModelPolicyPatch = {
        ...assignmentPatch(global),
        thinking_overrides: props.settingsStore.state.modelPolicy?.thinking_overrides ?? [],
      };
      const updated = await props.client.updateModelPolicySettings(next, projectId);
      props.settingsStore.actions.setModelPolicy(updated);
    } catch (err) {
      props.settingsStore.actions.setError(
        err instanceof Error ? err.message : String(err),
      );
    } finally {
      setBusyOverride(false);
    }
  };

  return (
    <div
      class="den-settings-editor--project den-settings-editor"
      data-testid="models-editor"
      data-scope="project"
    >
      <SettingsEditorTitle
        icon={<ProvidersIcon />}
        scopeBadge={PROJECT_SETTINGS_OVERLAY_COPY.badge}
      >
        {AI_PROVIDERS_SECTION_LABEL}
      </SettingsEditorTitle>

      <SettingsScopeLede
        scope="project"
        counterpartLabel={props.counterpartLabel}
        onOpenCounterpart={props.onOpenCounterpart}
      >
        <p class="den-settings-hint">{MODELS_SETTINGS_COPY.modelPolicyIntro}</p>
      </SettingsScopeLede>
      <ProjectSettingsOverrideControl
        settingsPath={PROJECT_SETTINGS_OVERLAY_COPY.settingsPath.providers}
        enabled={projectOverrideEnabled()}
        disabled={busyOverride()}
        followingSummary={settingsSummary()}
        onChange={(enabled) => void applyProjectOverride(enabled)}
      />

      <Show when={projectOverrideEnabled()}>
        {/* No `by`: each onChange spreads a new draft, so keying on it would
            rebuild the panel — focus and open pickers included — per keystroke. */}
        <ShowLatest when={draft()}>
          {(policySnap) => (
            <ModelPolicyPanel
              policy={policySnap()}
              providers={providers()}
              hasStoredAssignments={hasStoredAssignments()}
              saving={autoSave.saving() || busyOverride()}
              onChange={setDraft}
              projectLocal
            />
          )}
        </ShowLatest>
      </Show>
      <Show when={inheritanceError()}><p class="den-settings-warn" role="alert">{inheritanceError()}</p></Show>
      <Show when={inheritedPolicy()}><ShowLatest when={props.settingsStore.state.modelPolicy}>{(policySnap) => <ThinkingOverridesPanel
        policy={policySnap()}
        inherited={inheritedPolicy()}
        scopeKey={props.projectDir}
        providers={providers()}
        project
        saving={autoSave.saving() || busyOverride()}
        onChange={async (thinking_overrides) => {
          const { projectId } = projectScope();
          props.settingsStore.actions.setError(undefined);
          autoSave.cancel();
          setBusyOverride(true);
          try {
            const assignments = draft();
            const stored = props.settingsStore.state.modelPolicy;
            const changed = assignments && stored && !modelPoliciesEqual({ ...assignments, thinking_overrides: [] }, { ...stored, thinking_overrides: [] });
            const updated = await props.client.updateModelPolicySettings({
              ...(changed ? assignmentPatch(assignments) : {}),
              thinking_overrides,
            }, projectId);
            if (projectScope().projectId === projectId) props.settingsStore.actions.setModelPolicy(updated);
          } finally { setBusyOverride(false); }
        }}
      />}</ShowLatest></Show>
    </div>
  );
}
