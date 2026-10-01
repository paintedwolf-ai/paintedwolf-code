import { Show, createEffect, createSignal, untrack } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { DenButton } from "../../components/primitives/DenButton.tsx";
import { DenField } from "../../components/primitives/DenField.tsx";
import { DenInput } from "../../components/primitives/DenInput.tsx";
import { SettingsEditorTitle, TestsIcon } from "../../components/settings/SettingsEditorTitle.tsx";
import { overlayRel } from "../../platform/files/overlay-dir.ts";
import { VERIFY_SETTINGS_COPY } from "../../settings/extensions/verify-settings-copy.ts";

type Props = {
  client: LycaonClient;
  appStore: AppStore;
  projectId: string;
  projectDir?: string;
};

export function VerifyEditor(props: Props) {
  const query = createSurfaceQuery({
    name: "verify-settings",
    source: () => ({ client: props.client, projectId: props.projectId,
      key: `${props.projectId}:${props.appStore.state.verifyDetectRevision}`, connection: props.appStore.state.sidecarStatus, revision: props.appStore.state.verifyDetectRevision }),
    scope: (source) => source.projectId,
    load: ({ client, projectId }) => client.getVerifySettings(projectId),
  });
  const doc = query.value;
  const [draft, setDraft] = createSignal("");
  const loading = query.coldPending;
  const [saving, setSaving] = createSignal(false);
  const [actionError, setError] = createSignal<string | null>(null);
  const error = () => actionError() ?? query.error();

  const backendLive = () => doc()?.backend_configured === true;
  const dirty = () => draft().trim() !== (doc()?.test ?? "").trim();

  let priorTest: string | undefined;
  let priorProject: string | undefined;
  createEffect(() => {
    const settings = doc();
    if (!settings) return;
    const projectId = props.projectId;
    untrack(() => {
      if (priorProject !== projectId || draft() === priorTest) setDraft(settings.test);
      priorProject = projectId;
      priorTest = settings.test;
    });
  });

  const save = async (test: string) => {
    if (!backendLive()) return;
    const target = query.capture();
    setSaving(true);
    setError(null);
    try {
      const updated = await props.client.updateVerifySettings({ test }, props.projectId);
      target.publish(updated);
      if (target.current()) setDraft(updated.test);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const acceptDetected = () => {
    const detected = doc()?.detected_command?.trim();
    if (!detected) return;
    setDraft(detected);
  };

  return (
    <div class="verify-editor den-settings-editor" data-testid="verify-editor">
      <header class="den-settings-header">
        <SettingsEditorTitle icon={<TestsIcon />} optional>
          Tests
        </SettingsEditorTitle>
        <p class="den-settings-hint" data-testid="verify-editor-hint">
          {VERIFY_SETTINGS_COPY.editorHint}
        </p>
      </header>

        <Show when={error()}>
          <p class="den-settings-warn" data-testid="verify-editor-error" role="alert">
            {error()}
          </p>
        </Show>

      <Show when={query.showLoading()}>
        <p class="den-settings-hint">Loading…</p>
      </Show>

      <Show when={!loading() && !error() && !backendLive()}>
        <p class="den-settings-hint" data-testid="verify-editor-disabled">
          Verify settings are not available yet. Attach a project folder and
          connect to the backend to configure a test command.
        </p>
      </Show>

      <Show when={!loading() && backendLive()}>
        <DenField label="Test command">
          <DenInput
            class="verify-editor__command-input"
            data-testid="verify-editor-command"
            value={draft()}
            disabled={saving()}
            onInput={(e) => setDraft(e.currentTarget.value)}
          />
        </DenField>
        <p class="verify-editor__path den-settings-hint" data-testid="verify-editor-path">
          Saved to {doc()?.verify_path ?? overlayRel("verify.yaml")}
        </p>

        <Show when={!doc()?.test.trim() && doc()?.detected_command}>
          <div class="verify-editor__detect-banner" data-testid="verify-editor-detect-banner">
            <p>
              Found <code>{doc()?.detected_command}</code> in{" "}
              {doc()?.detected_source ?? "your project docs"} — proposes only, never
              applies silently.
            </p>
            <DenButton
              variant="secondary"
              data-testid="verify-editor-accept-detected"
              onClick={acceptDetected}
            >
              Use detected command
            </DenButton>
          </div>
        </Show>



        <div class="den-settings-actions">
          <DenButton
            variant="primary"
            data-testid="verify-editor-save"
            disabled={saving() || !dirty()}
            onClick={() => void save(draft().trim())}
          >
            {saving() ? "Saving…" : "Save"}
          </DenButton>
          <DenButton
            variant="secondary"
            data-testid="verify-editor-clear"
            disabled={saving() || !doc()?.test.trim()}
            onClick={() => void save("")}
          >
            Clear
          </DenButton>
        </div>
      </Show>
    </div>
  );
}
