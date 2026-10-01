import { createProviderActions } from "./provider-actions.ts";
import { settingControl, settingLabel } from "../../../settings/settings-registry.ts";
import { ChromeDragSurface } from "../../shell/ChromeDragSurface.tsx";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { For, Show, createEffect, createSignal } from "solid-js";
import { createStore } from "solid-js/store";
import { ResidentPortal } from "../../primitives/ResidentPortal.tsx";
import type { Accessor, JSX } from "solid-js";
import { createModalFocusTrap, shellChromeInertTargets } from "../../../platform/interaction/modal-focus-trap.ts";
import type { LycaonClient } from "../../../api/client.ts";
import type { ProviderMeta } from "../../../api/types.ts";
import { groupProvidersByKind, providerDiscoveryFailure, providerDisplayName, providerEndpointHost, providerIsReady, providerKindIsCustom, providerKindTemplateGroups, providerShowsApiKeyControls, providerStatusLabel, providerToolCallSupport } from "../../../settings/providers/models-editor-model.ts";
import { ambientAuthCopy, kindIsAmbientOnly, kindOffersCredentialChoice, providerCredentialSource, templateForProvider } from "../../../settings/providers/provider-credential-modes.ts";
import { MODELS_SETTINGS_COPY } from "../../../settings/providers/models-settings-copy.ts";
import { providerEndpointError } from "../../../settings/providers/cloudflare-endpoint.ts";
import type { SettingsStore } from "../../../store/settings-store.ts";
import { InlineRenameInput } from "../../inline-rename/InlineRenameInput.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenField } from "../../primitives/DenField.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { DenRadio } from "../../primitives/DenRadio.tsx";
import { Scrollport } from "../../primitives/Scrollport.tsx";
import { ShowLatest } from "../../primitives/ShowLatest.tsx";
import { SettingsListBackChrome } from "../SettingsListBackChrome.tsx";
import { SettingsListChrome } from "../SettingsListChrome.tsx";
import { SettingsListGroup } from "../SettingsListGroup.tsx";
import { ListSurfaceInbox } from "../../list/ListSurfaceInbox.tsx";
import { SettingsListPanel } from "../SettingsListPanel.tsx";
import { SettingsListRow } from "../SettingsListRow.tsx";
import { CloudflareAccountField } from "./CloudflareAccountField.tsx";
import { PROVIDER_LABEL_MAX, type NewProviderForm, emptyForm, endpointStyle, endpointLabel, parseConfiguredModels, formShowsApiKeyField, toolCallStateCopy, secretScreenTrustEndpointNote, providerFeedStatusHints, flattenGroupedProviders } from "./provider-form-model.ts";

type Props = {
  client: LycaonClient;
  settingsStore: SettingsStore;
  providers: readonly ProviderMeta[];
  onRemoveProvider: (id: string) => void | Promise<void>;
  /** Parent can open the Add AI provider dialog. */
  addOpen?: boolean;
  onAddOpenChange?: (open: boolean) => void;
};

export function ProvidersPanel(props: Props) {
  const { card, patchCard, onKindChange, addProvider, saveEndpoint, saveModels, setCredentialMode, setSecretScreenTrust, renameLabel, saveCredential, clearCredential, testProvider, refreshModels, toggleModelList, removeProvider, observeProviders } = createProviderActions({
    client: () => props.client,
    providers: () => props.providers,
    settingsStore: () => props.settingsStore,
    form: () => form,
    setForm: (patch) => setForm(patch),
    kinds: () => kinds(),
    selectedTemplate: () => selectedTemplate(),
    existingIds: () => existingIds(),
    selectedId: () => selectedId(),
    setSelectedId: (id) => setSelectedId(id),
    setShowAddForm: (visible) => setShowAddForm(visible),
    notifyAddOpenChange: (visible) => props.onAddOpenChange?.(visible),
    removeProviderFromSettings: (id) => props.onRemoveProvider(id),
  });
  const [form, setForm] = createStore<NewProviderForm>(emptyForm());
  const kinds = () => props.settingsStore.state.providerKinds ?? [];
  const [selectedId, setSelectedId] = createSignal<string | null>(null);
  const [renaming, setRenaming] = createSignal(false);
  const [showAddForm, setShowAddForm] = createSignal(false);
  const [kindQuery, setKindQuery] = createSignal("");
  let addDialogRef: HTMLDivElement | undefined;
  let kindFilterRef: HTMLInputElement | undefined;
  let providerLabelRef: HTMLInputElement | undefined;

  createEffect(() => {
    void props.client
      .listProviderKinds()
      .then(props.settingsStore.actions.setProviderKinds)
      .catch((err: unknown) => {
        props.settingsStore.actions.setError(
          err instanceof Error ? err.message : String(err),
        );
      });
  });

  observeProviders();

  createEffect(() => {
    if (props.addOpen) setShowAddForm(true);
  });

  createEffect(() => {
    const visible = flattenGroupedProviders(groups());
    const current = selectedId();
    if (current && !visible.some((p) => p.id === current)) {
      setSelectedId(null);
    }
  });

  const groups = () => groupProvidersByKind(props.providers, kinds());
  const kindGroups = () => providerKindTemplateGroups(kinds());
  const filteredKindGroups = () => {
    const q = kindQuery().trim().toLowerCase();
    if (!q) return kindGroups();
    return kindGroups()
      .map((group) => ({
        ...group,
        templates: group.templates.filter(
          (t) =>
            t.label.toLowerCase().includes(q) ||
            t.kind.toLowerCase().includes(q),
        ),
      }))
      .filter((group) => group.templates.length > 0);
  };
  const existingIds = () => new Set(props.providers.map((p) => p.id));
  const selectedTemplate = () => kinds().find((k) => k.kind === form.kind);
  const selectedProvider = () =>
    props.providers.find((p) => p.id === selectedId()) ?? null;

  const closeDetail = () => {
    setRenaming(false);
    setSelectedId(null);
  };

  const closeAddDialog = () => {
    setShowAddForm(false);
    props.onAddOpenChange?.(false);
    setForm(emptyForm());
    setKindQuery("");
  };

  const selectProvider = (id: string) => {
    closeAddDialog();
    setRenaming(false);
    setSelectedId(id);
  };

  const openAddForm = () => {
    setKindQuery("");
    setForm(emptyForm());
    setShowAddForm(true);
    props.onAddOpenChange?.(true);
  };

  createModalFocusTrap(showAddForm, () => addDialogRef, {
    onEscape: () => {
      if (!form.busy) closeAddDialog();
    },
    inertTarget: () =>
      document.getElementById("root") ?? shellChromeInertTargets(),
  });

  createEffect(() => {
    if (!showAddForm() || form.kind) return;
    queueMicrotask(() => kindFilterRef?.focus());
  });

  createEffect(() => {
    if (!showAddForm() || !form.kind) return;
    queueMicrotask(() => providerLabelRef?.focus());
  });

  const providerCount = () =>
    flattenGroupedProviders(groups()).length;

  const clearKind = () => {
    setForm(emptyForm());
  };

  const renderAddConfigureFields = (): JSX.Element => (
    <Scrollport
      class="den-settings-add-dialog__body"
      contentClass="den-settings-add-dialog__body-content"
      eager
      data-testid="add-provider-form"
    >
      <Show when={form.error}>
        <p class="den-settings-warn" role="alert">{form.error}</p>
      </Show>
      <button
        type="button"
        class="den-settings-add-dialog__picked"
        data-testid="add-provider-change-kind"
        onClick={() => clearKind()}
      >
        ← {selectedTemplate()?.label ?? form.kind}
      </button>
      <DenField label={MODELS_SETTINGS_COPY.providerLabelLabel}>
        <DenInput
          ref={(el) => {
            providerLabelRef = el;
          }}
          type="text"
          autocomplete="off"
          data-testid="add-provider-label"
          placeholder={MODELS_SETTINGS_COPY.providerLabelPlaceholder}
          value={form.label}
          onInput={(e) => setForm("label", e.currentTarget.value)}
        />
      </DenField>
      <Show
        when={endpointStyle(selectedTemplate()?.endpoint_style) !== "derived"}
      >
        <Show when={form.kind === "cloudflare-workers-ai"}>
          <CloudflareAccountField
            baseUrl={form.baseUrl}
            testId="add-provider-account-id"
            onInput={(baseUrl) => setForm("baseUrl", baseUrl)}
          />
        </Show>
        <DenField
          label={endpointLabel(
            endpointStyle(selectedTemplate()?.endpoint_style),
          )}
        >
          <DenInput
            type="text"
            autocomplete="off"
            data-testid="add-provider-base-url"
            aria-invalid={!!providerEndpointError(form.kind, form.baseUrl)}
            aria-describedby={
              providerEndpointError(form.kind, form.baseUrl)
                ? "add-provider-endpoint-error"
                : undefined
            }
            placeholder={
              endpointStyle(selectedTemplate()?.endpoint_style) === "region"
                ? "us-east-1"
                : "https://…"
            }
            value={form.baseUrl}
            onInput={(e) => setForm("baseUrl", e.currentTarget.value)}
          />
        </DenField>
        <Show when={providerEndpointError(form.kind, form.baseUrl)}>
          {(error) => (
            <p class="den-settings-warn" id="add-provider-endpoint-error" role="status">
              {error()}
            </p>
          )}
        </Show>
      </Show>
      <Show
        when={endpointStyle(selectedTemplate()?.endpoint_style) === "derived"}
      >
        <p
          class="den-settings-hint"
          data-testid="add-provider-derived-endpoint-hint"
        >
          {MODELS_SETTINGS_COPY.providerDerivedEndpointHint}
        </p>
      </Show>
      <Show when={form.kind === "azure"}>
        <DenField label={MODELS_SETTINGS_COPY.providerDeploymentsLabel}>
          <DenInput
            type="text"
            autocomplete="off"
            data-testid="add-provider-models"
            placeholder={MODELS_SETTINGS_COPY.providerDeploymentsPlaceholder}
            value={form.models}
            onInput={(e) => setForm("models", e.currentTarget.value)}
          />
        </DenField>
        <p class="den-settings-hint">
          {MODELS_SETTINGS_COPY.providerDeploymentsHint}
        </p>
      </Show>
      <Show when={providerKindIsCustom(form.kind)}>
        <DenCheckbox
          data-testid="add-provider-require-api-key"
          checked={form.requireApiKey}
          onChange={(e) => setForm("requireApiKey", e.currentTarget.checked)}
        >
          {MODELS_SETTINGS_COPY.providerRequireApiKey}
        </DenCheckbox>
      </Show>
      <Show when={kindOffersCredentialChoice(selectedTemplate())}>
        <DenField label={MODELS_SETTINGS_COPY.providerCredentialLabel}>
          <div
            class="den-settings-credential-modes"
            role="radiogroup"
            aria-label={MODELS_SETTINGS_COPY.providerCredentialLabel}
            data-testid="add-provider-credential-mode"
          >
            <DenRadio
              name="add-provider-credential-mode"
              data-testid="add-provider-credential-mode-api-key"
              checked={form.requireApiKey}
              onChange={() => setForm("requireApiKey", true)}
            >
              {MODELS_SETTINGS_COPY.providerApiKeyLabel}
            </DenRadio>
            <DenRadio
              name="add-provider-credential-mode"
              data-testid="add-provider-credential-mode-ambient"
              checked={!form.requireApiKey}
              onChange={() => setForm({ requireApiKey: false, apiKey: "" })}
            >
              {ambientAuthCopy(selectedTemplate()?.ambient_auth)?.label ?? ""}
            </DenRadio>
          </div>
        </DenField>
        <Show when={!form.requireApiKey}>
          <p class="den-settings-hint" data-testid="add-provider-ambient-hint">
            {ambientAuthCopy(selectedTemplate()?.ambient_auth)?.detail ?? ""}
          </p>
        </Show>
      </Show>
      <Show when={kindIsAmbientOnly(selectedTemplate())}>
        <p class="den-settings-hint" data-testid="add-provider-ambient-hint">
          {ambientAuthCopy(selectedTemplate()?.ambient_auth)?.detail ?? ""}
        </p>
      </Show>
      <Show when={formShowsApiKeyField(form.kind, selectedTemplate(), form.requireApiKey)}>
        <DenField label={MODELS_SETTINGS_COPY.providerApiKeyLabel}>
          <DenInput
            type="password"
            autocomplete="off"
            class="den-settings-api-key-input"
            data-testid="add-provider-api-key"
            placeholder={MODELS_SETTINGS_COPY.providerApiKeyPlaceholder}
            value={form.apiKey}
            onInput={(e) => setForm("apiKey", e.currentTarget.value)}
          />
        </DenField>
        <p class="den-settings-hint" data-testid="add-provider-api-key-hint">
          {MODELS_SETTINGS_COPY.providerApiKeyOptionalHint}
        </p>
      </Show>
    </Scrollport>
  );

  const renderAddDialog = (): JSX.Element => (
    <Show when={showAddForm()}>
      <ResidentPortal>
        <div
          class="den-dialog-backdrop den-dialog-backdrop--viewport"
          data-testid="providers-add-dialog"
          data-settings-overlay="true"
          onClick={(e) => {
            if (e.target === e.currentTarget && !form.busy) {
              closeAddDialog();
            }
          }}
        >
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <div
            ref={addDialogRef}
            class="den-dialog den-dialog--sheet"
            role="dialog"
            aria-modal="true"
            aria-labelledby="providers-add-title"
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="providers-add-title">
                {MODELS_SETTINGS_COPY.newProviderHeading}
              </h2>
            </header>
            <Show
              when={kindGroups().length > 0}
              fallback={
                <p class="den-dialog__hint">
                  {MODELS_SETTINGS_COPY.noProvidersLoaded}
                </p>
              }
            >
            <Show
              when={form.kind}
              fallback={
                <>
                  <DenInput
                    ref={(el) => {
                      kindFilterRef = el;
                    }}
                    class="den-settings-add-dialog__filter"
                    type="search"
                    autocomplete="off"
                    data-testid="add-provider-filter"
                    placeholder={MODELS_SETTINGS_COPY.filterProviders}
                    value={kindQuery()}
                    onInput={(e) => setKindQuery(e.currentTarget.value)}
                  />
                  <Show
                    when={filteredKindGroups().length > 0}
                    fallback={
                      <p class="den-dialog__hint">
                        {MODELS_SETTINGS_COPY.noFilterMatches}
                      </p>
                    }
                  >
                    <Scrollport
                      class="den-settings-add-dialog__kind-scroll"
                      contentAs="ul"
                      contentClass="den-settings-add-dialog__kind-list"
                      eager
                      data-testid="add-provider-kind-list"
                    >
                      <For each={filteredKindGroups()}>
                        {(group) => (
                          <>
                            <li class="den-settings-add-dialog__group">
                              {group.label}
                            </li>
                            <For each={group.templates}>
                              {(k) => (
                                <li>
                                  <button
                                    type="button"
                                    class="den-dialog__row"
                                    data-testid={`add-provider-kind-${k.kind}`}
                                    onClick={() => onKindChange(k.kind)}
                                  >
                                    <span class="den-dialog__row-title">
                                      {k.label}
                                    </span>
                                  </button>
                                </li>
                              )}
                            </For>
                          </>
                        )}
                      </For>
                    </Scrollport>
                  </Show>
                </>
              }
            >
              {renderAddConfigureFields()}
            </Show>
            </Show>
            <footer class="den-settings-add-dialog__footer">
              <DenButton
                variant="secondary"
                compact
                data-testid="add-provider-cancel"
                disabled={form.busy}
                onClick={() => closeAddDialog()}
              >
                {MODELS_SETTINGS_COPY.cancel}
              </DenButton>
              <Show when={form.kind}>
                <DenButton
                  variant="primary"
                  compact
                  data-testid="add-provider-btn"
                  disabled={
                    form.busy ||
                    !!providerEndpointError(form.kind, form.baseUrl) ||
                    (endpointStyle(selectedTemplate()?.endpoint_style) !==
                      "derived" &&
                      !form.baseUrl.trim()) ||
                    (form.kind === "azure" &&
                      parseConfiguredModels(form.models).length === 0)
                  }
                  onClick={() => void addProvider()}
                >
                  {MODELS_SETTINGS_COPY.addProvider}
                </DenButton>
              </Show>
            </footer>
          </div>
        </div>
      </ResidentPortal>
    </Show>
  );

  // The live accessor updates fields without remounting the card.
  const renderProviderBody = (provider: Accessor<ProviderMeta>): JSX.Element => (
    <>
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
        {(failure) => (
          <div
            class="den-settings-provider-refresh-failure"
            role="status"
            data-testid={`provider-discovery-failure-${provider().id}`}
          >
            <p class="den-settings-warn">{failure.title}</p>
            <p class="den-settings-hint">{failure.detail}</p>
            <p class="den-settings-hint">{failure.nextStep}</p>
          </div>
        )}
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
            baseUrl={card(provider().id).baseUrl}
            testId={`provider-account-id-${provider().id}`}
            onInput={(baseUrl) => patchCard(provider().id, { baseUrl })}
          />
        </Show>
        <DenField label={endpointLabel(endpointStyle(provider().endpoint_style))}>
          <DenInput
            type="text"
            autocomplete="off"
            data-testid={`provider-endpoint-${provider().id}`}
            aria-invalid={!!providerEndpointError(provider().kind, card(provider().id).baseUrl)}
            aria-describedby={
              providerEndpointError(provider().kind, card(provider().id).baseUrl)
                ? `provider-endpoint-error-${provider().id}`
                : undefined
            }
            placeholder={
              endpointStyle(provider().endpoint_style) === "region"
                ? "us-east-1"
                : (provider().base_url ?? "https://…")
            }
            value={card(provider().id).baseUrl}
            onInput={(e) =>
              patchCard(provider().id, { baseUrl: e.currentTarget.value })
            }
          />
        </DenField>
        <Show when={providerEndpointError(provider().kind, card(provider().id).baseUrl)}>
          {(error) => (
            <p class="den-settings-warn" id={`provider-endpoint-error-${provider().id}`} role="status">
              {error()}
            </p>
          )}
        </Show>
        <div class="den-settings-provider-actions">
          <DenButton
            variant="secondary"
            data-testid={`provider-save-endpoint-${provider().id}`}
            disabled={
              card(provider().id).savingEndpoint ||
              !!providerEndpointError(provider().kind, card(provider().id).baseUrl) ||
              !card(provider().id).baseUrl.trim()
            }
            onClick={() => void saveEndpoint(provider())}
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
            value={card(provider().id).models}
            onInput={(e) =>
              patchCard(provider().id, { models: e.currentTarget.value })
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
            disabled={card(provider().id).savingModels}
            onClick={() => void saveModels(provider())}
          >
            {MODELS_SETTINGS_COPY.saveDeployments}
          </DenButton>
        </div>
      </Show>

      <Show
        when={kindOffersCredentialChoice(
          templateForProvider(provider(), kinds()),
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
              onChange={() => void setCredentialMode(provider(), true)}
            >
              {MODELS_SETTINGS_COPY.providerApiKeyLabel}
            </DenRadio>
            <DenRadio
              name={`provider-credential-mode-${provider().id}`}
              data-testid={`provider-credential-mode-ambient-${provider().id}`}
              checked={!provider().requires_api_key}
              onChange={() => void setCredentialMode(provider(), false)}
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
            value={card(provider().id).apiKey}
            onInput={(e) =>
              patchCard(provider().id, {
                apiKey: e.currentTarget.value,
              })
            }
          />
        </DenField>
        <div class="den-settings-provider-actions">
          <DenButton
            variant="secondary"
            data-testid={`provider-save-key-${provider().id}`}
            onClick={() => void saveCredential(provider())}
          >
            {MODELS_SETTINGS_COPY.saveApiKey}
          </DenButton>
          <Show when={provider().credential_present}>
            <DenButton
              variant="secondary"
              data-testid={`provider-clear-key-${provider().id}`}
              onClick={() => void clearCredential(provider())}
            >
              {MODELS_SETTINGS_COPY.clearApiKey}
            </DenButton>
          </Show>
        </div>
      </Show>

      <DenCheckbox
        data-testid={`provider-secret-trust-${provider().id}`}
        checked={provider().secret_screen_trusted ?? false}
        disabled={card(provider().id).savingSecretTrust}
        onChange={(e) =>
          void setSecretScreenTrust(provider(), e.currentTarget.checked)
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
            onClick={() => void toggleModelList(provider())}
          >
            {card(provider().id).expanded ? "Hide list" : "Show list"}
          </DenButton>
        </p>
        <Show when={card(provider().id).expanded}>
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
            disabled={card(provider().id).testing}
            onClick={() => void testProvider(provider())}
          >
            {card(provider().id).testing
              ? MODELS_SETTINGS_COPY.testingConnection
              : MODELS_SETTINGS_COPY.testConnection}
          </DenButton>
          <DenButton
            variant="secondary"
            data-testid={`provider-refresh-${provider().id}`}
            disabled={card(provider().id).refreshing}
            onClick={() => void refreshModels(provider())}
          >
            {card(provider().id).refreshing
              ? MODELS_SETTINGS_COPY.refreshingModels
              : MODELS_SETTINGS_COPY.refreshModels}
          </DenButton>
        </div>
        <Show when={card(provider().id).testMessage}>
          <p
            class="den-settings-hint"
            data-testid={`provider-test-result-${provider().id}`}
          >
            {card(provider().id).testMessage}
          </p>
        </Show>
        {/* Keep refresh errors below the retained model list. */}
        <Show when={card(provider().id).refreshFailure} keyed>
          {(failure) => (
            <div
              class="den-settings-provider-refresh-failure"
              role="status"
              data-testid={`provider-refresh-failure-${provider().id}`}
            >
              <p class="den-settings-warn">{failure.title}</p>
              <p class="den-settings-hint">{failure.message}</p>
              <Show when={failure.suggestedAction}>
                <p class="den-settings-hint">{failure.suggestedAction}</p>
              </Show>
              <DenButton
                variant="secondary"
                data-testid={`provider-refresh-retry-${provider().id}`}
                disabled={card(provider().id).refreshing}
                onClick={() => void refreshModels(provider())}
              >
                {MODELS_SETTINGS_COPY.retryRefreshModels}
              </DenButton>
            </div>
          )}
        </Show>
      </Show>
    </>
  );

  const renderSettingsInbox = (): JSX.Element => {
    const detailOpen = () => selectedProvider() != null;
    const isEmpty = () => groups().length === 0;

    return (
      <>
        <SettingsListPanel
          testId="providers-list-panel"
          setting="ai-providers"
          chrome={
            detailOpen() ? (
              <SettingsListBackChrome
                testId="providers-back"
                label={MODELS_SETTINGS_COPY.providersBack}
                onBack={closeDetail}
              />
            ) : (
              <SettingsListChrome
                testId="providers-list-chrome"
                count={
                  <span data-testid="providers-count">
                    {providerCount()} AI provider
                    {providerCount() === 1 ? "" : "s"}
                  </span>
                }
                action={
                  <DenButton
                    variant="primary"
                    compact
                    data-testid="providers-add"
                    {...settingControl()}
                    onClick={openAddForm}
                  >
                    {settingLabel("ai-providers")}
                  </DenButton>
                }
              />
            )
          }
        >
          <ListSurfaceInbox
            class="den-settings-providers"
            detailOpen={detailOpen()}
            isEmpty={isEmpty()}
            listTestId="providers-list"
            detailTestId="providers-detail"
            empty={
              <p
                class="den-settings-hint den-settings-list-inbox__empty"
                data-testid="providers-empty"
              >
                {MODELS_SETTINGS_COPY.providersEmpty}
              </p>
            }
            list={
              <For each={groups()}>
                {(group) => (
                  <SettingsListGroup
                    testId={`provider-group-${group.kind}`}
                    label={group.label}
                    meta={
                      group.providers.length > 1
                        ? MODELS_SETTINGS_COPY.instanceCount(
                            group.providers.length,
                          )
                        : undefined
                    }
                  >
                    <For each={group.providers}>
                      {(provider) => (
                        <SettingsListRow
                          testId={`providers-row-${provider.id}`}
                          primary={providerDisplayName(provider)}
                          secondary={providerEndpointHost(provider.base_url)}
                          status={
                            <span
                              class="den-settings-provider-status"
                              data-configured={providerIsReady(provider)}
                            >
                              {providerStatusLabel(provider)}
                            </span>
                          }
                          onSelect={() => selectProvider(provider.id)}
                        />
                      )}
                    </For>
                  </SettingsListGroup>
                )}
              </For>
            }
            detail={
              <div class="den-settings-providers-detail">
                {/* Provider identity preserves the detail form across refreshes. */}
                <ShowLatest
                  when={props.providers.find((p) => p.id === selectedId())}
                  by={(provider) => provider.id}
                >
                  {(provider) => (
                    <article
                      class="den-settings-provider-card"
                      data-testid={`provider-card-${provider().id}`}
                    >
                      <header class="den-settings-provider-head">
                        <div class="den-settings-provider-title">
                          <Show
                            when={!renaming()}
                            fallback={
                              <InlineRenameInput
                                class="den-settings-provider-rename-input"
                                testId="provider-rename-input"
                                initialValue={providerDisplayName(provider())}
                                maxLength={PROVIDER_LABEL_MAX}
                                ariaLabel={MODELS_SETTINGS_COPY.renameDisplayName}
                                onCommit={(next) => {
                                  setRenaming(false);
                                  void renameLabel(provider(), next);
                                }}
                                onCancel={() => setRenaming(false)}
                              />
                            }
                          >
                            <button
                              type="button"
                              class="den-settings-provider-rename-trigger"
                              data-testid="provider-rename-trigger"
                              aria-label={MODELS_SETTINGS_COPY.renameDisplayName}
                              data-tip={MODELS_SETTINGS_COPY.renameDisplayName}
                              onClick={() => setRenaming(true)}
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
                          onClick={() => void removeProvider(provider())}
                        >
                          {MODELS_SETTINGS_COPY.removeProvider}
                        </DenButton>
                      </header>
                      <p class="den-settings-provider-label-hint">
                        {MODELS_SETTINGS_COPY.providerLabelRenameHint}
                      </p>
                      {renderProviderBody(provider)}
                    </article>
                  )}
                </ShowLatest>
              </div>
            }
          />
        </SettingsListPanel>
        {renderAddDialog()}
      </>
    );
  };

  return renderSettingsInbox();
}
