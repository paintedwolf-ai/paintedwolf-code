import { createProviderActions } from "./provider-actions.ts";
import { settingControl, settingLabel } from "../../../settings/settings-registry.ts";
import { ChromeDragSurface } from "../../shell/ChromeDragSurface.tsx";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { For, Show, createEffect, createSignal } from "solid-js";
import { createStore } from "solid-js/store";
import { ResidentPortal } from "../../primitives/ResidentPortal.tsx";
import type { JSX } from "solid-js";
import { createModalFocusTrap, shellChromeInertTargets } from "../../../platform/interaction/modal-focus-trap.ts";
import type { LycaonClient } from "../../../api/client.ts";
import type { ProviderMeta } from "../../../api/types.ts";
import {
  groupProvidersByKind,
  providerDisplayName,
  providerEndpointHost,
  providerIsReady,
  providerKindIsCustom,
  providerKindTemplateGroups,
  providerStatusLabel,
} from "../../../settings/providers/models-editor-model.ts";
import {
  ambientAuthCopy,
  kindIsAmbientOnly,
  kindOffersCredentialChoice,
} from "../../../settings/providers/provider-credential-modes.ts";
import { MODELS_SETTINGS_COPY } from "../../../settings/providers/models-settings-copy.ts";
import { providerEndpointError } from "../../../settings/providers/cloudflare-endpoint.ts";
import type { SettingsStore } from "../../../store/settings-store.ts";
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
import { ProviderDetailCard } from "./ProviderDetailCard.tsx";
import {
  type NewProviderForm,
  emptyForm,
  endpointStyle,
  endpointLabel,
  parseConfiguredModels,
  formShowsApiKeyField,
  flattenGroupedProviders,
} from "./provider-form-model.ts";

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
                  <ProviderDetailCard
                    provider={provider}
                    kinds={kinds}
                    renaming={renaming}
                    setRenaming={setRenaming}
                    card={card}
                    patchCard={patchCard}
                    onRenameLabel={(item, next) => void renameLabel(item, next)}
                    onRemoveProvider={(item) => void removeProvider(item)}
                    onSaveEndpoint={(item) => void saveEndpoint(item)}
                    onSaveModels={(item) => void saveModels(item)}
                    onSetCredentialMode={(item, reqKey) => void setCredentialMode(item, reqKey)}
                    onSaveCredential={(item) => void saveCredential(item)}
                    onClearCredential={(item) => void clearCredential(item)}
                    onSetSecretScreenTrust={(item, trusted) => void setSecretScreenTrust(item, trusted)}
                    onToggleModelList={(item) => void toggleModelList(item)}
                    onTestProvider={(item) => void testProvider(item)}
                    onRefreshModels={(item) => void refreshModels(item)}
                    renderDiscoveryFailure={(providerId, failure) => (
                      <div
                        class="den-settings-provider-refresh-failure"
                        role="status"
                        data-testid={`provider-discovery-failure-${providerId}`}
                      >
                        <p class="den-settings-warn">{failure.title}</p>
                        <p class="den-settings-hint">{failure.detail}</p>
                        <p class="den-settings-hint">{failure.nextStep}</p>
                      </div>
                    )}
                    renderEndpointError={(providerId, error) => (
                      <p
                        class="den-settings-warn"
                        id={`provider-endpoint-error-${providerId}`}
                        role="status"
                      >
                        {error}
                      </p>
                    )}
                    renderRefreshFailure={(item, failure) => (
                      <div
                        class="den-settings-provider-refresh-failure"
                        role="status"
                        data-testid={`provider-refresh-failure-${item.id}`}
                      >
                        <p class="den-settings-warn">{failure.title}</p>
                        <p class="den-settings-hint">{failure.message}</p>
                        <Show when={failure.suggestedAction}>
                          <p class="den-settings-hint">{failure.suggestedAction}</p>
                        </Show>
                        <DenButton
                          variant="secondary"
                          data-testid={`provider-refresh-retry-${item.id}`}
                          disabled={card(item.id).refreshing}
                          onClick={() => void refreshModels(item)}
                        >
                          {MODELS_SETTINGS_COPY.retryRefreshModels}
                        </DenButton>
                      </div>
                    )}
                  />
                )}
              </ShowLatest>
            </div>
          }
        />
      </SettingsListPanel>
      {renderAddDialog()}
    </>
  );
}
