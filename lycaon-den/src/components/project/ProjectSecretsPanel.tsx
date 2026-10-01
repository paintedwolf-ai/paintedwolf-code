import { Show, createMemo, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { PROJECT_SETTINGS_OVERLAY_COPY } from "../../settings/security/project-settings-overlay-copy.ts";
import { BrowseSegmented } from "../browse/BrowseSegmented.tsx";
import { SurfaceDeck } from "../primitives/SurfaceDeck.tsx";
import { SecretsIcon, SettingsEditorTitle } from "../settings/SettingsEditorTitle.tsx";
import { SettingsScopeLede } from "../settings/SettingsScopeLede.tsx";
import { IgnoredSecretValues } from "./IgnoredSecretValues.tsx";
import { ManagedSecretsPanel } from "./ManagedSecretsPanel.tsx";

export function ProjectSecretsPanel(props: { client: LycaonClient; projectId: string }) {
  const [view, setView] = createSignal("managed");
  let generation = 0;
  const scopeGeneration = createMemo(() => {
    props.client; props.projectId;
    return ++generation;
  });

  return (
    <div class="den-settings-editor den-settings-editor--project" data-testid="project-secrets-panel">
      <SettingsEditorTitle icon={<SecretsIcon />} scopeBadge={PROJECT_SETTINGS_OVERLAY_COPY.badge}>Secrets</SettingsEditorTitle>
      <SettingsScopeLede scope="project">Manage protected credentials and the public values this project can ignore.</SettingsScopeLede>
      <div>
        <BrowseSegmented
          ariaLabel="Secrets view"
          value={view()}
          onChange={setView}
          options={[
            { id: "managed", label: "Managed secrets" },
            { id: "ignored", label: "Ignored values" },
          ]}
        />
      </div>
      <SurfaceDeck active={view()} generation={() => scopeGeneration()}>
        {(section) => (
          <Show when={section === "managed"} fallback={<IgnoredSecretValues client={props.client} projectId={props.projectId} />}>
            <ManagedSecretsPanel client={props.client} projectId={props.projectId} />
          </Show>
        )}
      </SurfaceDeck>
    </div>
  );
}
