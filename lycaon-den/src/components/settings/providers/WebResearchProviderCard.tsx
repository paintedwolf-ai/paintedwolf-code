import { For, Show } from "solid-js";
import type { WebResearchProviderMeta } from "../../../api/types.ts";
import {
  providerIsReady,
  providerStatusLabel,
} from "../../../settings/research/web-research-providers-model.ts";
import {
  catalogEntry,
  type WebResearchCatalogEntry,
  type WebResearchProviderId,
} from "../../../settings/web-research-catalog.generated.ts";
import { WEB_RESEARCH_SETTINGS_COPY } from "../../../settings/research/web-research-settings-copy.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenField } from "../../primitives/DenField.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { WebResearchCredentialField } from "./WebResearchCredentialField.tsx";
import type { ProviderCardDraft } from "./WebResearchProvidersPanel.tsx";

type Props = {
  meta: WebResearchProviderMeta;
  draft: ProviderCardDraft;
  extraFields: NonNullable<WebResearchCatalogEntry["extra_fields"]>;
  onPatch: (patch: Partial<ProviderCardDraft>) => void;
  onSaveCredential: () => void;
  onClearCredential: () => void;
  onSaveConfig: () => void;
  onTest: () => void;
  onRemove: () => void;
};

function showsEndpoint(meta: WebResearchProviderMeta): boolean {
  return meta.kind === "keyless_endpoint";
}

function showsExtraFields(meta: WebResearchProviderMeta): boolean {
  return meta.kind === "keyed_extra";
}

export function WebResearchProviderCard(props: Props) {
  return (
    <article
      class="den-settings-provider-card"
      data-testid={`web-research-card-${props.meta.id}`}
    >
      <header class="den-settings-provider-head">
        <strong>
          {WEB_RESEARCH_SETTINGS_COPY.providerLabel(
            props.meta.id as WebResearchProviderId,
          )}
        </strong>
        <span
          class="den-settings-provider-status"
          data-configured={providerIsReady(props.meta)}
        >
          {providerStatusLabel(props.meta)}
        </span>
        <DenButton
          variant="link"
          class="den-settings-provider-remove"
          data-testid={`web-research-remove-${props.meta.id}`}
          onClick={() => props.onRemove()}
        >
          {WEB_RESEARCH_SETTINGS_COPY.remove}
        </DenButton>
      </header>

      <p class="den-settings-hint">
        {WEB_RESEARCH_SETTINGS_COPY.providerHint(
          props.meta.id as WebResearchProviderId,
        )}
      </p>

      <WebResearchCredentialField
        meta={props.meta}
        draft={props.draft}
        onPatch={props.onPatch}
        onSave={props.onSaveCredential}
        onClear={props.onClearCredential}
      />

      <Show when={showsExtraFields(props.meta)}>
        <For each={props.extraFields}>
          {(field) => (
            <DenField label={field.label}>
              <DenInput
                type="text"
                autocomplete="off"
                data-testid={`web-research-extra-${props.meta.id}-${field.name}`}
                placeholder={props.meta.config?.[field.name] ?? ""}
                value={props.draft.extra[field.name] ?? ""}
                onInput={(e) =>
                  props.onPatch({
                    extra: { ...props.draft.extra, [field.name]: e.currentTarget.value },
                  })
                }
              />
            </DenField>
          )}
        </For>
      </Show>

      <Show when={showsEndpoint(props.meta)}>
        <DenField label="Endpoint">
          <DenInput
            type="text"
            autocomplete="off"
            data-testid={`web-research-endpoint-${props.meta.id}`}
            placeholder={
              catalogEntry(props.meta.id as WebResearchProviderId).default_endpoint ||
              WEB_RESEARCH_SETTINGS_COPY.endpointPlaceholder
            }
            value={props.draft.endpoint}
            onInput={(e) => props.onPatch({ endpoint: e.currentTarget.value })}
          />
        </DenField>
        <Show
          when={
            props.meta.allow_private_endpoint ||
            catalogEntry(props.meta.id as WebResearchProviderId).allow_private_endpoint
          }
        >
          <p
            class="den-settings-hint"
            data-testid={`web-research-private-endpoint-warn-${props.meta.id}`}
          >
            {WEB_RESEARCH_SETTINGS_COPY.privateEndpointWarning}
          </p>
        </Show>
      </Show>

      <Show when={showsExtraFields(props.meta) || showsEndpoint(props.meta)}>
        <div class="den-settings-provider-actions">
          <DenButton
            variant="secondary"
            data-testid={`web-research-save-config-${props.meta.id}`}
            disabled={props.draft.savingConfig}
            onClick={() => props.onSaveConfig()}
          >
            {WEB_RESEARCH_SETTINGS_COPY.saveConfig}
          </DenButton>
        </div>
      </Show>

      <div class="den-settings-provider-actions">
        <DenButton
          variant="secondary"
          data-testid={`web-research-test-${props.meta.id}`}
          disabled={props.draft.testing}
          onClick={() => props.onTest()}
        >
          {props.draft.testing
            ? WEB_RESEARCH_SETTINGS_COPY.testing
            : WEB_RESEARCH_SETTINGS_COPY.testSearch}
        </DenButton>
      </div>

      <Show when={props.draft.testMessage}>
        <p
          class="den-settings-hint"
          data-testid={`web-research-test-result-${props.meta.id}`}
        >
          {props.draft.testMessage}
        </p>
      </Show>

      <Show when={props.draft.error}>
        <p class="den-settings-warn" role="alert">{props.draft.error}</p>
      </Show>
    </article>
  );
}
