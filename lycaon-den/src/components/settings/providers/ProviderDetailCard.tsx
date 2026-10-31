import { For, Show, type Accessor, type JSX } from "solid-js";
import type { ProviderKindTemplate, ProviderMeta } from "../../../api/types.ts";
import { providerEndpointError } from "../../../settings/providers/cloudflare-endpoint.ts";
import {
  providerDiscoveryFailure,
  providerDisplayName,
  providerIsReady,
  providerShowsApiKeyControls,
  providerStatusLabel,
  providerToolCallSupport,
} from "../../../settings/providers/models-editor-model.ts";
import { MODELS_SETTINGS_COPY } from "../../../settings/providers/models-settings-copy.ts";
import {
  ambientAuthCopy,
  kindOffersCredentialChoice,
  providerCredentialSource,
  templateForProvider,
} from "../../../settings/providers/provider-credential-modes.ts";
import { InlineRenameInput } from "../../inline-rename/InlineRenameInput.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { DenField } from "../../primitives/DenField.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { DenRadio } from "../../primitives/DenRadio.tsx";
import { CloudflareAccountField } from "./CloudflareAccountField.tsx";
import {
  PROVIDER_LABEL_MAX,
  endpointLabel,
  endpointStyle,
  providerFeedStatusHints,
  secretScreenTrustEndpointNote,
  toolCallStateCopy,
  type ProviderCardDraft,
} from "./provider-form-model.ts";

export type ProviderDetailCardProps = {
  provider: Accessor<ProviderMeta>;
  kinds: () => readonly ProviderKindTemplate[];
  renaming: () => boolean;
  setRenaming: (renaming: boolean) => void;
  card: (id: string) => ProviderCardDraft;
  patchCard: (id: string, patch: Partial<ProviderCardDraft>) => void;
  onRenameLabel: (provider: ProviderMeta, next: string) => void;
  onRemoveProvider: (provider: ProviderMeta) => void;
  onSaveEndpoint: (provider: ProviderMeta) => void;
  onSaveModels: (provider: ProviderMeta) => void;
  onSetCredentialMode: (provider: ProviderMeta, requireApiKey: boolean) => void;
  onSaveCredential: (provider: ProviderMeta) => void;
  onClearCredential: (provider: ProviderMeta) => void;
  onSetSecretScreenTrust: (provider: ProviderMeta, trusted: boolean) => void;
  onToggleModelList: (provider: ProviderMeta) => void;
  onTestProvider: (provider: ProviderMeta) => void;
  onRefreshModels: (provider: ProviderMeta) => void;
  renderDiscoveryFailure: (providerId: string, failure: NonNullable<ReturnType<typeof providerDiscoveryFailure>>) => JSX.Element;
  renderEndpointError: (providerId: string, error: string) => JSX.Element;
  renderRefreshFailure: (
    provider: ProviderMeta,
    failure: NonNullable<ProviderCardDraft["refreshFailure"]>,
  ) => JSX.Element;
};

export function ProviderDetailCard(props: ProviderDetailCardProps) {
  const provider = () => props.provider();

  // The live accessor updates fields without remounting the card.
  return (
    <article
      class="den-settings-provider-card"
      data-testid={`provider-card-${provider().id}`}
    >
      <header class="den-settings-provider-head">
        <div class="den-settings-provider-title">
          <Show
            when={!props.renaming()}
            fallback={
              <InlineRenameInput
                class="den-settings-provider-rename-input"
                testId="provider-rename-input"
                initialValue={providerDisplayName(provider())}
                maxLength={PROVIDER_LABEL_MAX}
                ariaLabel={MODELS_SETTINGS_COPY.renameDisplayName}
                onCommit={(next) => {
                  props.setRenaming(false);
                  props.onRenameLabel(provider(), next);
                }}
                onCancel={() => props.setRenaming(false)}
              />
            }
          >
            <button
              type="button"
              class="den-settings-provider-rename-trigger"
              data-testid="provider-rename-trigger"
              aria-label={MODELS_SETTINGS_COPY.renameDisplayName}
              data-tip={MODELS_SETTINGS_COPY.renameDisplayName}
              onClick={() => props.setRenaming(true)}
            >
              {providerDisplayName(provider())}
            </button>
          </Show>
          <code
            class="den-settings-provider-id"
            data-testid={`provider-id-${provider().id}`}
          >
            {provider().id}
          </code>
        </div>
        <span
          class="den-settings-provider-status"
          data-configured={providerIsReady(provider())}
        >
          {providerStatusLabel(provider())}
        </span>
        <DenButton
          variant="link"
          class="den-settings-provider-remove"
          data-testid={`provider-remove-${provider().id}`}
          onClick={() => props.onRemoveProvider(provider())}
        >
          {MODELS_SETTINGS_COPY.removeProvider}
        </DenButton>
      </header>
      <p class="den-settings-provider-label-hint">
        {MODELS_SETTINGS_COPY.providerLabelRenameHint}
      </p>

      <Show when={providerFeedStatusHints(provider()).length > 0}>
        <p
          class="den-settings-hint"
          data-testid={`provider-feed-status-${provider().id}`}
        >
          {providerFeedStatusHints(provider()).join(" · ")}
        </p>
      </Show>

      <Show when={provider().usage_quota}>
        {(quota) => (
          <p
            class="den-settings-hint"
            data-testid={`provider-usage-quota-${provider().id}`}
          >
            {MODELS_SETTINGS_COPY.usageQuota(
              quota().used,
              quota().limit,
              quota().unit,
            )}
            <Show when={(quota().overage_nano_usd ?? 0) > 0}>
              {" "}
              ({MODELS_SETTINGS_COPY.usageQuotaOverage((quota().overage_nano_usd ?? 0) / 1e9)})
            </Show>
          </p>
        )}
      </Show>

      {/* Reuses the provider-card failure block: same role, same card. */}
      <Show when={providerDiscoveryFailure(provider())} keyed>
        {(failure) => props.renderDiscoveryFailure(provider().id, failure)}
      </Show>

      {/* Unknown is a hint, not a warning: nothing has said no. */}
      <Show when={providerToolCallSupport(provider()) !== "supported"}>
        <p
          class="den-settings-hint"
          classList={{
            "den-settings-warn":
              providerToolCallSupport(provider()) === "unsupported",
          }}
          data-testid={`provider-tool-calls-${provider().id}`}
          data-tool-calls={providerToolCallSupport(provider())}
        >
          {toolCallStateCopy(provider())}
        </p>
      </Show>

      <Show when={endpointStyle(provider().endpoint_style) !== "derived"}>
        <Show when={provider().kind === "cloudflare-workers-ai"}>
          <CloudflareAccountField
            baseUrl={props.card(provider().id).baseUrl}
            testId={`provider-account-id-${provider().id}`}
            onInput={(baseUrl) => props.patchCard(provider().id, { baseUrl })}
          />
        </Show>
        <DenField label={endpointLabel(endpointStyle(provider().endpoint_style))}>
          <DenInput
            type="text"
            autocomplete="off"
            data-testid={`provider-endpoint-${provider().id}`}
            aria-invalid={!!providerEndpointError(provider().kind, props.card(provider().id).baseUrl)}
            aria-describedby={
              providerEndpointError(provider().kind, props.card(provider().id).baseUrl)
                ? `provider-endpoint-error-${provider().id}`
                : undefined
            }
            placeholder={
              endpointStyle(provider().endpoint_style) === "region"
                ? "us-east-1"
                : (provider().base_url ?? "https://…")
            }
            value={props.card(provider().id).baseUrl}
            onInput={(e) =>
              props.patchCard(provider().id, { baseUrl: e.currentTarget.value })
            }
          />
        </DenField>
        <Show when={providerEndpointError(provider().kind, props.card(provider().id).baseUrl)}>
          {(error) => props.renderEndpointError(provider().id, error())}
        </Show>
        <div class="den-settings-provider-actions">
          <DenButton
            variant="secondary"
            data-testid={`provider-save-endpoint-${provider().id}`}
            disabled={
              props.card(provider().id).savingEndpoint ||
              !!providerEndpointError(provider().kind, props.card(provider().id).baseUrl) ||
              !props.card(provider().id).baseUrl.trim()
            }
            onClick={() => props.onSaveEndpoint(provider())}
          >
            {MODELS_SETTINGS_COPY.saveEndpoint}
          </DenButton>
        </div>
      </Show>
      <Show when={endpointStyle(provider().endpoint_style) === "derived"}>
        <p class="den-settings-hint">
          {MODELS_SETTINGS_COPY.providerDerivedEndpointHint}
        </p>
      </Show>
      <Show when={provider().kind === "azure"}>
        <DenField label={MODELS_SETTINGS_COPY.providerDeploymentsLabel}>
          <DenInput
            type="text"
            autocomplete="off"
            data-testid={`provider-models-${provider().id}`}
            placeholder={MODELS_SETTINGS_COPY.providerDeploymentsPlaceholder}
            value={props.card(provider().id).models}
            onInput={(e) =>
              props.patchCard(provider().id, { models: e.currentTarget.value })
            }
          />
        </DenField>
        <p class="den-settings-hint">
          {MODELS_SETTINGS_COPY.providerDeploymentsHint}
        </p>
        <div class="den-settings-provider-actions">
          <DenButton
            variant="secondary"
            data-testid={`provider-save-models-${provider().id}`}
            disabled={props.card(provider().id).savingModels}
            onClick={() => props.onSaveModels(provider())}
          >
            {MODELS_SETTINGS_COPY.saveDeployments}
          </DenButton>
        </div>
      </Show>

      <Show
        when={kindOffersCredentialChoice(
          templateForProvider(provider(), props.kinds()),
        )}
      >
        <DenField label={MODELS_SETTINGS_COPY.providerCredentialLabel}>
          <div
            class="den-settings-credential-modes"
            role="radiogroup"
            aria-label={MODELS_SETTINGS_COPY.providerCredentialLabel}
            data-testid={`provider-credential-mode-${provider().id}`}
          >
            <DenRadio
              name={`provider-credential-mode-${provider().id}`}
              data-testid={`provider-credential-mode-api-key-${provider().id}`}
              checked={provider().requires_api_key}
              onChange={() => props.onSetCredentialMode(provider(), true)}
            >
              {MODELS_SETTINGS_COPY.providerApiKeyLabel}
            </DenRadio>
            <DenRadio
              name={`provider-credential-mode-${provider().id}`}
              data-testid={`provider-credential-mode-ambient-${provider().id}`}
              checked={!provider().requires_api_key}
              onChange={() => props.onSetCredentialMode(provider(), false)}
            >
              {ambientAuthCopy(provider().ambient_auth)?.label ?? ""}
            </DenRadio>
          </div>
        </DenField>
      </Show>
      <Show
        when={
          !provider().requires_api_key &&
          ambientAuthCopy(provider().ambient_auth)
        }
      >
        {(copy) => (
          <p
            class="den-settings-hint"
            data-testid={`provider-ambient-hint-${provider().id}`}
            data-ambient-resolved={
              providerCredentialSource(provider()) === "ambient"
            }
          >
            {copy().detail}{" "}
            {providerCredentialSource(provider()) === "ambient"
              ? MODELS_SETTINGS_COPY.ambientCredentialsActive
              : MODELS_SETTINGS_COPY.ambientCredentialsMissing}
          </p>
        )}
      </Show>

      <Show when={providerShowsApiKeyControls(provider())}>
        <DenField
          label={
            <span class="den-settings-provider-key-label">
              <span>{MODELS_SETTINGS_COPY.providerApiKeyLabel}</span>
              <span
                class="den-settings-provider-status"
                classList={{
                  "den-settings-warn":
                    providerCredentialSource(provider()) === "none" &&
                    provider().requires_api_key,
                }}
                data-configured={
                  providerCredentialSource(provider()) !== "none"
                }
                data-source={providerCredentialSource(provider())}
                data-testid={`provider-key-status-${provider().id}`}
              >
                {providerCredentialSource(provider()) === "stored"
                  ? MODELS_SETTINGS_COPY.apiKeyStoredStatus
                  : MODELS_SETTINGS_COPY.apiKeyMissingStatus}
              </span>
            </span>
          }
        >
          <DenInput
            type="password"
            autocomplete="off"
            class="den-settings-api-key-input"
            data-testid={`provider-api-key-${provider().id}`}
            data-credential={
              provider().credential_present ? "stored" : "missing"
            }
            data-required={
              provider().requires_api_key ? "true" : "false"
            }
            placeholder={
              provider().credential_present
                ? MODELS_SETTINGS_COPY.providerApiKeyPlaceholderStored
                : MODELS_SETTINGS_COPY.providerApiKeyPlaceholder
            }
            value={props.card(provider().id).apiKey}
            onInput={(e) =>
              props.patchCard(provider().id, {
                apiKey: e.currentTarget.value,
              })
            }
          />
        </DenField>
        <div class="den-settings-provider-actions">
          <DenButton
            variant="secondary"
            data-testid={`provider-save-key-${provider().id}`}
            onClick={() => props.onSaveCredential(provider())}
          >
            {MODELS_SETTINGS_COPY.saveApiKey}
          </DenButton>
          <Show when={provider().credential_present}>
            <DenButton
              variant="secondary"
              data-testid={`provider-clear-key-${provider().id}`}
              onClick={() => props.onClearCredential(provider())}
            >
              {MODELS_SETTINGS_COPY.clearApiKey}
            </DenButton>
          </Show>
        </div>
      </Show>

      <DenCheckbox
        data-testid={`provider-secret-trust-${provider().id}`}
        checked={provider().secret_screen_trusted ?? false}
        disabled={props.card(provider().id).savingSecretTrust}
        onChange={(e) =>
          props.onSetSecretScreenTrust(provider(), e.currentTarget.checked)
        }
      >
        {MODELS_SETTINGS_COPY.secretScreenTrustLabel}
      </DenCheckbox>
      <p
        class="den-settings-hint"
        data-testid={`provider-secret-trust-hint-${provider().id}`}
      >
        {MODELS_SETTINGS_COPY.secretScreenTrustHint}
        <Show when={secretScreenTrustEndpointNote(provider())}>
          {(note) => <> {note()}</>}
        </Show>
      </p>

      <Show when={provider().models.length > 0}>
        <p class="den-settings-hint">
          {provider().models.length} model
          {provider().models.length === 1 ? "" : "s"}
          {" · "}
          <DenButton
            variant="link"
            data-testid={`provider-toggle-models-${provider().id}`}
            onClick={() => props.onToggleModelList(provider())}
          >
            {props.card(provider().id).expanded ? "Hide list" : "Show list"}
          </DenButton>
        </p>
        <Show when={props.card(provider().id).expanded}>
          <ul class="den-settings-provider-models" data-testid={`provider-model-list-${provider().id}`}>
            <For each={provider().models}>{model => <li><code>{model.id}</code><p class="den-settings-hint">{model.eligibility.coordinator.reason}</p></li>}</For>
          </ul>
        </Show>
      </Show>

      <Show when={provider().configured}>
        <p class="den-settings-hint" data-testid={`provider-test-scope-${provider().id}`}>
          {MODELS_SETTINGS_COPY.testConnectionScopeHint}
          <Show when={provider().kind === "cloudflare-workers-ai"}>
            {" "}{MODELS_SETTINGS_COPY.cloudflareModelAccessHint}
          </Show>
        </p>
        <div class="den-settings-provider-actions">
          <DenButton
            variant="secondary"
            data-testid={`provider-test-${provider().id}`}
            disabled={props.card(provider().id).testing}
            onClick={() => props.onTestProvider(provider())}
          >
            {props.card(provider().id).testing
              ? MODELS_SETTINGS_COPY.testingConnection
              : MODELS_SETTINGS_COPY.testConnection}
          </DenButton>
          <DenButton
            variant="secondary"
            data-testid={`provider-refresh-${provider().id}`}
            disabled={props.card(provider().id).refreshing}
            onClick={() => props.onRefreshModels(provider())}
          >
            {props.card(provider().id).refreshing
              ? MODELS_SETTINGS_COPY.refreshingModels
              : MODELS_SETTINGS_COPY.refreshModels}
          </DenButton>
        </div>
        <Show when={props.card(provider().id).testMessage}>
          <p
            class="den-settings-hint"
            data-testid={`provider-test-result-${provider().id}`}
          >
            {props.card(provider().id).testMessage}
          </p>
        </Show>
        {/* Keep refresh errors below the retained model list. */}
        <Show when={props.card(provider().id).refreshFailure} keyed>
          {(failure) => props.renderRefreshFailure(provider(), failure)}
        </Show>
      </Show>
    </article>
  );
}
