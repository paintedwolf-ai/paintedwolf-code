import { createSettingsDraft } from "../../../settings/settings-draft.ts";
import { Index, Show, createEffect } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { ContentReviewRule } from "../../../api/types.ts";
import type { AppStore } from "../../../store/app-state-model.ts";
import type { SettingsStore } from "../../../store/settings-store.ts";
import { createSettingsAutoSave } from "../../../settings/settings-auto-save.ts";
import { SETTINGS_EDITOR_COPY } from "../../../settings/editor/settings-editor-copy.ts";
import { resolveEditorSettingsScope } from "../../../settings/settings-scope.ts";
import type { createProjectsStore } from "../../../store/projects-store.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { SettingsEditorTitle, EditReviewIcon } from "../SettingsEditorTitle.tsx";
import { chromeProps } from "../../../styling/ui-chrome.ts";

type ProjectsStore = ReturnType<typeof createProjectsStore>;

type Props = {
  client: LycaonClient;
  appStore: AppStore;
  settingsStore: SettingsStore;
  projects: ProjectsStore;
  projectDir?: string;
  alwaysProjectScope?: boolean;
};

function cloneRules(rules: readonly ContentReviewRule[]): ContentReviewRule[] {
  return JSON.parse(JSON.stringify(rules)) as ContentReviewRule[];
}

export function EditReviewSettingsPanel(props: Props) {
  const draftState = createSettingsDraft<ContentReviewRule[]>([]);
  const rules = draftState.value;
  const setRules = draftState.set;

  const autoSave = createSettingsAutoSave({
    isReady: () => props.settingsStore.state.review != null,
    isDirty: () => {
      const review = props.settingsStore.state.review;
      if (!review) return false;
      return JSON.stringify(review.review_paths) !== JSON.stringify(rules());
    },
    save: async () => {
      const { projectId } = resolveEditorSettingsScope(
        props.alwaysProjectScope,
        props.projectDir,
        props.projects.state.projects,
      );
      props.settingsStore.actions.setError(undefined);
      try {
        const updated = await props.client.updateReviewSettings({ review_paths: rules() }, projectId);
        props.settingsStore.actions.setReview(updated);
      } catch (err) {
        props.settingsStore.actions.setError(
          err instanceof Error ? err.message : String(err),
        );
      }
    },
  });

  createEffect(() => {
    const review = props.settingsStore.state.review;
    if (!review) return;
    draftState.receive(cloneRules(review.review_paths), autoSave.runSynced);
  });

  createEffect(() => {
    rules();
    autoSave.schedule();
  });

  const addRule = () => {
    setRules((prev) => [...prev, { tool: "", path: "" }]);
  };

  const updateRule = (index: number, patch: Partial<ContentReviewRule>) => {
    setRules((prev) =>
      prev.map((row, i) => (i === index ? { ...row, ...patch } : row)),
    );
  };

  const removeRule = (index: number) => {
    setRules((prev) => prev.filter((_, i) => i !== index));
  };

  return (
    <div class="den-settings-editor" data-testid="edit-review-settings">
      <SettingsEditorTitle icon={<EditReviewIcon />}>Edit review</SettingsEditorTitle>
      <p class="den-settings-hint">
        Edits whose path matches a rule below pause for review before they land on
        disk. Everything else applies directly.
      </p>

      <section class="den-settings-section">
        <h3 class="den-settings-subhead" {...chromeProps()}>Review these paths</h3>
        <p class="den-settings-hint">
          Path glob is required; leave Tool blank to review any edit to that path.
        </p>
        <ul class="den-settings-rules den-settings-rules-pairs">
          <li class="den-settings-rules-head" aria-hidden="true">
            <span>Tool (optional)</span>
            <span>Path glob</span>
            <span />
          </li>
          <Index each={rules()}>
            {(rule, index) => (
              <li>
                <DenInput
                  kind="rules"
                  aria-label="Tool"
                  placeholder="any"
                  value={rule().tool ?? ""}
                  onInput={(e) =>
                    updateRule(index, { tool: e.currentTarget.value })
                  }
                />
                <DenInput
                  kind="rules"
                  aria-label="Path glob"
                  placeholder="infra/**"
                  value={rule().path}
                  onInput={(e) =>
                    updateRule(index, { path: e.currentTarget.value })
                  }
                />
                <DenButton variant="danger" onClick={() => removeRule(index)}>
                  Remove
                </DenButton>
              </li>
            )}
          </Index>
        </ul>
        <div class="den-settings-actions">
          <DenButton
            variant="secondary"
            data-testid="edit-review-add-rule"
            onClick={addRule}
          >
            Add path
          </DenButton>
        </div>
      </section>

      <Show when={props.settingsStore.state.review?.merged_from}>
        {(mergedFrom) => (
          <p class="den-settings-hint">Merged from: {mergedFrom().join(", ")}</p>
        )}
      </Show>

      <Show when={autoSave.saving()}>
        <p class="den-settings-hint" data-testid="edit-review-saving">
          {SETTINGS_EDITOR_COPY.saving}
        </p>
      </Show>
    </div>
  );
}
