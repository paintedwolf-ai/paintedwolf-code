import { settingControl, settingLabel } from "../../../settings/settings-registry.ts";
import { ChromeDragSurface } from "../../shell/ChromeDragSurface.tsx";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { createEffect, createSignal, For, Show } from "solid-js";
import { createStore } from "solid-js/store";
import { ResidentPortal } from "../../primitives/ResidentPortal.tsx";
import type { LycaonClient } from "../../../api/client.ts";
import { createModalFocusTrap } from "../../../platform/interaction/modal-focus-trap.ts";
import type {
  WebResearchProviderMeta,
  WebResearchProvidersResponse,
} from "../../../api/types.ts";
import {
  DIRECT_PROVIDER_ID,
  directStatusLabel,
  groupAddProviderCandidates,
  groupWebResearchProvidersByKind,
  hasAddProviderCandidates,
  providerIdIsDirectBundled,
  providerIsReady,
  providerStatusLabel,
} from "../../../settings/research/web-research-providers-model.ts";
import {
  catalogEntry,
  type WebResearchProviderId,
} from "../../../settings/web-research-catalog.generated.ts";
import { WEB_RESEARCH_SETTINGS_COPY } from "../../../settings/research/web-research-settings-copy.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { ShowLatest } from "../../primitives/ShowLatest.tsx";
import { Scrollport } from "../../primitives/Scrollport.tsx";
import { SettingsListBackChrome } from "../SettingsListBackChrome.tsx";
import { SettingsListChrome } from "../SettingsListChrome.tsx";
import { ListSurfaceInbox } from "../../list/ListSurfaceInbox.tsx";
import { SettingsListPanel } from "../SettingsListPanel.tsx";
import { SettingsListRow } from "../SettingsListRow.tsx";
import { WebResearchProviderCard } from "./WebResearchProviderCard.tsx";
import {
  type WebResearchCredentialDraft,
  WebResearchCredentialField,
} from "./WebResearchCredentialField.tsx";

export type ProviderCardDraft = WebResearchCredentialDraft & {
  endpoint: string;
  extra: Record<string, string>;
  testing: boolean;
  savingConfig: boolean;
  testMessage?: string;
  error?: string;
};

type AddForm = {
  providerId: string;
  busy: boolean;
};

type Props = {
  client: LycaonClient;
  status: WebResearchProvidersResponse;
  enabledIds: ReadonlySet<string>;
  domainGuess: boolean;
  onDomainGuessChange: (on: boolean) => void;
  onStatus: (status: WebResearchProvidersResponse) => void;
  onAddProvider: (id: string) => void;
  onRemoveProvider: (id: string) => void;
};

type ListEntry =
  | { kind: "direct" }
  | { kind: "catalog"; meta: WebResearchProviderMeta };

function emptyCard(): ProviderCardDraft {
  return {
    apiKey: "",
    endpoint: "",
    extra: {},
    testing: false,
    savingKey: false,
    savingConfig: false,
  };
}

function cardFromMeta(meta: WebResearchProviderMeta): ProviderCardDraft {
  const config = meta.config ?? {};
  const extra = { ...config };
  delete extra.endpoint;
  return {
    ...emptyCard(),
    endpoint: config.endpoint ?? "",
    extra,
  };
}

function credentialSlot(meta: WebResearchProviderMeta): string | undefined {
  return meta.credential_slot;
}

export function WebResearchProvidersPanel(props: Props) {
  const [cardUI, setCardUI] = createStore<Record<string, ProviderCardDraft>>(
    {},
  );
  const [form, setForm] = createStore<AddForm>({ providerId: "", busy: false });
  const [directTesting, setDirectTesting] = createSignal(false);
  const [directTestMessage, setDirectTestMessage] = createSignal<
    string | undefined
  >();
  const [selectedId, setSelectedId] = createSignal<string | null>(null);
  const [showAddForm, setShowAddForm] = createSignal(false);
  const [addQuery, setAddQuery] = createSignal("");
  let addFilterRef: HTMLInputElement | undefined;
  let addDialogRef: HTMLDivElement | undefined;

  createEffect(() => {
    for (const provider of props.status.providers) {
      if (cardUI[provider.id] === undefined) {
        setCardUI(provider.id, cardFromMeta(provider));
      }
    }
  });

  const card = (id: string): ProviderCardDraft => cardUI[id] ?? emptyCard();

  const patchCard = (id: string, patch: Partial<ProviderCardDraft>) => {
    if (cardUI[id] === undefined) {
      setCardUI(id, { ...emptyCard(), ...patch });
      return;
    }
    for (
      const [key, value] of Object.entries(patch) as [
        keyof ProviderCardDraft,
        ProviderCardDraft[keyof ProviderCardDraft],
      ][]
    ) {
      setCardUI(id, key, value);
    }
  };

  const groups = () =>
    groupWebResearchProvidersByKind(props.status.providers, props.enabledIds);
  const addGroups = () =>
    groupAddProviderCandidates(
      props.enabledIds,
      props.status.direct.card.label,
    );
  const filteredAddGroups = () => {
    const q = addQuery().trim().toLowerCase();
    if (!q) return addGroups();
    return addGroups()
      .map((group) => ({
        ...group,
        options: group.options.filter(
          (entry) =>
            entry.label.toLowerCase().includes(q) ||
            entry.id.toLowerCase().includes(q),
        ),
      }))
      .filter((group) => group.options.length > 0);
  };
  const directEnabled = () => props.enabledIds.has(DIRECT_PROVIDER_ID);
  const directCredentialProviders = () => {
    const seenSlots = new Set<string>();
    return props.status.providers.filter((provider) => {
      const slot = provider.credential_slot;
      if (
        !providerIdIsDirectBundled(provider.id) || !slot || seenSlots.has(slot)
      ) {
        return false;
      }
      seenSlots.add(slot);
      return true;
    });
  };

  const listEntries = (): ListEntry[] => {
    const entries: ListEntry[] = [];
    if (directEnabled()) entries.push({ kind: "direct" });
    for (const group of groups()) {
      for (const meta of group.providers) {
        entries.push({ kind: "catalog", meta });
      }
    }
    return entries;
  };

  const providerCount = () => listEntries().length;
  const isEmpty = () => providerCount() === 0;

  createEffect(() => {
    const entries = listEntries();
    const current = selectedId();
    if (!current) return;
    if (current === DIRECT_PROVIDER_ID) {
      if (!directEnabled()) setSelectedId(null);
      return;
    }
    if (
      !entries.some((entry) =>
        entry.kind === "catalog" && entry.meta.id === current
      )
    ) {
      setSelectedId(null);
    }
  });

  const closeDetail = () => {
    setSelectedId(null);
  };

  const closeAddDialog = () => {
    setShowAddForm(false);
    setForm({ providerId: "", busy: false });
    setAddQuery("");
  };

  createModalFocusTrap(showAddForm, () => addDialogRef, {
    onEscape: () => {
      if (!form.busy) closeAddDialog();
    },
  });

  const openAddForm = () => {
    setAddQuery("");
    setShowAddForm(true);
  };

  const selectRow = (id: string) => {
    closeAddDialog();
    setSelectedId(id);
  };

  const detailOpen = () => selectedId() != null;

  createEffect(() => {
    if (!showAddForm()) return;
    queueMicrotask(() => addFilterRef?.focus());
  });

  const reload = async () => {
    props.onStatus(await props.client.getWebResearchProviders());
  };

  const saveCredential = async (meta: WebResearchProviderMeta) => {
    const key = card(meta.id).apiKey.trim();
    if (!credentialSlot(meta) || !key) return;
    patchCard(meta.id, { savingKey: true, error: undefined });
    try {
      await props.client.replaceWebResearchCredential(meta.id, key);
      props.onStatus(await props.client.getWebResearchProviders());
      patchCard(meta.id, { apiKey: "", savingKey: false });
    } catch (err) {
      patchCard(meta.id, {
        savingKey: false,
        error: err instanceof Error
          ? err.message
          : WEB_RESEARCH_SETTINGS_COPY.saveError,
      });
    }
  };

  const clearCredential = async (meta: WebResearchProviderMeta) => {
    if (!credentialSlot(meta)) return;
    patchCard(meta.id, { savingKey: true, error: undefined });
    try {
      await props.client.deleteWebResearchCredential(meta.id);
      props.onStatus(await props.client.getWebResearchProviders());
      patchCard(meta.id, { savingKey: false });
    } catch (err) {
      patchCard(meta.id, {
        savingKey: false,
        error: err instanceof Error
          ? err.message
          : WEB_RESEARCH_SETTINGS_COPY.clearError,
      });
    }
  };

  const saveConfig = async (meta: WebResearchProviderMeta) => {
    const draft = card(meta.id);
    const config: Record<string, string> = {};
    for (const field of extraFields(meta)) {
      config[field.name] = draft.extra[field.name]?.trim() ?? "";
    }
    if (meta.kind === "keyless_endpoint") {
      config.endpoint = draft.endpoint.trim();
    }
    patchCard(meta.id, { savingConfig: true, error: undefined });
    try {
      await props.client.updateWebResearchProvider(meta.id, {
        config,
      });
      const next = await props.client.getWebResearchProviders();
      props.onStatus(next);
      const saved = next.providers.find((provider) => provider.id === meta.id);
      const savedDraft = saved ? cardFromMeta(saved) : undefined;
      patchCard(meta.id, {
        savingConfig: false,
        endpoint: savedDraft?.endpoint ?? draft.endpoint.trim(),
        extra: savedDraft?.extra ?? { ...draft.extra },
      });
    } catch (err) {
      patchCard(meta.id, {
        savingConfig: false,
        error: err instanceof Error
          ? err.message
          : WEB_RESEARCH_SETTINGS_COPY.configError,
      });
    }
  };

  const testProvider = async (meta: WebResearchProviderMeta) => {
    patchCard(meta.id, { testing: true, testMessage: undefined });
    try {
      const res = await props.client.testWebResearchProvider(meta.id);
      patchCard(meta.id, {
        testing: false,
        testMessage: res.ok
          ? `OK${res.latency_ms != null ? ` (${res.latency_ms} ms)` : ""}`
          : res.message ?? WEB_RESEARCH_SETTINGS_COPY.testError,
      });
    } catch (err) {
      patchCard(meta.id, {
        testing: false,
        testMessage: err instanceof Error
          ? err.message
          : WEB_RESEARCH_SETTINGS_COPY.testError,
      });
    }
  };

  const testDirect = async () => {
    setDirectTesting(true);
    setDirectTestMessage(undefined);
    try {
      const res = await props.client.testWebResearchProvider(
        DIRECT_PROVIDER_ID,
      );
      setDirectTestMessage(
        res.ok
          ? `OK${res.latency_ms != null ? ` (${res.latency_ms} ms)` : ""}`
          : res.message ?? WEB_RESEARCH_SETTINGS_COPY.testError,
      );
    } catch (err) {
      setDirectTestMessage(
        err instanceof Error
          ? err.message
          : WEB_RESEARCH_SETTINGS_COPY.testError,
      );
    } finally {
      setDirectTesting(false);
    }
  };

  const removeProvider = async (meta: WebResearchProviderMeta) => {
    props.onRemoveProvider(meta.id);
    if (meta.credential_present && credentialSlot(meta)) {
      await clearCredential(meta);
    }
    if (selectedId() === meta.id) setSelectedId(null);
    await reload();
  };

  const addProvider = (providerId?: string) => {
    const id = (providerId ?? form.providerId).trim();
    if (!id) return;
    setForm("busy", true);
    props.onAddProvider(id);
    setForm({ providerId: "", busy: false });
    setShowAddForm(false);
    setSelectedId(id);
  };

  const extraFields = (meta: WebResearchProviderMeta) => {
    const entry = catalogEntry(meta.id as WebResearchProviderId);
    return entry.extra_fields ?? [];
  };

  const renderAddDialog = () => (
    <Show when={showAddForm()}>
      <ResidentPortal mount={document.body}>
        <div
          class="den-dialog-backdrop den-dialog-backdrop--viewport"
          data-testid="web-research-add-dialog"
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
            aria-labelledby="web-research-add-title"
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="web-research-add-title">
                {WEB_RESEARCH_SETTINGS_COPY.addProviderHeading}
              </h2>
            </header>
            <Show
              when={hasAddProviderCandidates(props.enabledIds)}
              fallback={
                <p class="den-dialog__hint" data-testid="web-research-add-form">
                  {WEB_RESEARCH_SETTINGS_COPY.chooseProvider}
                </p>
              }
            >
              <DenInput
                ref={(el) => {
                  addFilterRef = el;
                }}
                class="den-settings-add-dialog__filter"
                type="search"
                autocomplete="off"
                data-testid="web-research-add-filter"
                placeholder={WEB_RESEARCH_SETTINGS_COPY.filterProviders}
                value={addQuery()}
                onInput={(e) => setAddQuery(e.currentTarget.value)}
              />
              <Show
                when={filteredAddGroups().length > 0}
                fallback={
                  <p
                    class="den-dialog__hint"
                    data-testid="web-research-add-form"
                  >
                    {WEB_RESEARCH_SETTINGS_COPY.noFilterMatches}
                  </p>
                }
              >
                <Scrollport
                  class="den-dialog__list"
                  contentAs="ul"
                  contentClass="den-dialog__list-content"
                  eager
                  data-testid="web-research-add-form"
                >
                  <For each={filteredAddGroups()}>
                    {(group) => (
                      <>
                        <li class="den-settings-add-dialog__group">
                          {group.label}
                        </li>
                        <For each={group.options}>
                          {(entry) => (
                            <li>
                              <button
                                type="button"
                                class="den-dialog__row"
                                data-testid={`web-research-add-option-${entry.id}`}
                                disabled={form.busy}
                                onClick={() => addProvider(entry.id)}
                              >
                                <span class="den-dialog__row-title">
                                  {entry.label}
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
            </Show>
            <footer class="den-settings-add-dialog__footer">
              <DenButton
                variant="secondary"
                compact
                disabled={form.busy}
                onClick={() => closeAddDialog()}
              >
                {WEB_RESEARCH_SETTINGS_COPY.cancel}
              </DenButton>
            </footer>
          </div>
        </div>
      </ResidentPortal>
    </Show>
  );

  const renderDirectDetail = () => (
    <article
      class="den-settings-provider-card"
      data-testid="web-research-direct-card"
    >
      <header class="den-settings-provider-head">
        <strong>{props.status.direct.card.label}</strong>
        <span
          class="den-settings-provider-status"
          data-configured={props.status.direct.configured}
        >
          {directStatusLabel(props.status.direct.configured)}
        </span>
        <DenButton
          variant="link"
          class="den-settings-provider-remove"
          data-testid="web-research-remove-direct"
          onClick={() => {
            props.onRemoveProvider(DIRECT_PROVIDER_ID);
            setSelectedId(null);
          }}
        >
          {WEB_RESEARCH_SETTINGS_COPY.remove}
        </DenButton>
      </header>
      <p class="den-settings-hint">{WEB_RESEARCH_SETTINGS_COPY.directHint}</p>
      <For each={directCredentialProviders()}>
        {(meta) => (
          <section data-testid={`web-research-direct-credential-${meta.id}`}>
            <strong>
              {WEB_RESEARCH_SETTINGS_COPY.providerLabel(
                meta.id as WebResearchProviderId,
              )}
            </strong>
            <p class="den-settings-hint">
              {WEB_RESEARCH_SETTINGS_COPY.providerHint(
                meta.id as WebResearchProviderId,
              )}
            </p>
            <WebResearchCredentialField
              meta={meta}
              draft={card(meta.id)}
              onPatch={(patch) =>
                patchCard(meta.id, patch)}
              onSave={() => void saveCredential(meta)}
              onClear={() => void clearCredential(meta)}
            />
            <Show when={card(meta.id).error}>
              <p class="den-settings-warn" role="alert">{card(meta.id).error}</p>
            </Show>
          </section>
        )}
      </For>
      <div
        class="den-settings-pref-row"
        data-testid="web-research-domain-guess-row"
      >
        <div
          class="den-settings-pref-copy"
          data-testid="web-research-direct-options"
        >
          <span class="den-settings-pref-label">
            {WEB_RESEARCH_SETTINGS_COPY.domainGuessLabel}
          </span>
          <p class="den-settings-hint">
            {WEB_RESEARCH_SETTINGS_COPY.domainGuessHint}
          </p>
        </div>
        <DenCheckbox
          checked={props.domainGuess}
          data-testid="web-research-domain-guess-toggle"
          onChange={(e) => props.onDomainGuessChange(e.currentTarget.checked)}
        >
          <span class="sr-only">
            {WEB_RESEARCH_SETTINGS_COPY.domainGuessLabel}
          </span>
        </DenCheckbox>
      </div>
      <div class="den-settings-provider-actions">
        <DenButton
          variant="secondary"
          data-testid="web-research-direct-test"
          disabled={directTesting()}
          onClick={() => void testDirect()}
        >
          {directTesting()
            ? WEB_RESEARCH_SETTINGS_COPY.testing
            : WEB_RESEARCH_SETTINGS_COPY.testSearch}
        </DenButton>
      </div>
      <Show when={directTestMessage()}>
        <p
          class="den-settings-hint"
          data-testid="web-research-direct-test-result"
        >
          {directTestMessage()}
        </p>
      </Show>
    </article>
  );

  return (
    <>
      <SettingsListPanel
        testId="web-research-list-panel"
        setting="web-research-providers"
        chrome={detailOpen()
          ? (
            <SettingsListBackChrome
              testId="web-research-back"
              label={WEB_RESEARCH_SETTINGS_COPY.providersBack}
              onBack={closeDetail}
            />
          )
          : (
            <SettingsListChrome
              testId="web-research-list-chrome"
              count={
                <span data-testid="web-research-count">
                  {providerCount()} provider
                  {providerCount() === 1 ? "" : "s"}
                </span>
              }
              action={
                <DenButton
                  variant="primary"
                  compact
                  data-testid="web-research-add"
                  {...settingControl()}
                  onClick={() => openAddForm()}
                >
                  {settingLabel("web-research-providers")}
                </DenButton>
              }
            />
          )}
      >
        <ListSurfaceInbox
          detailOpen={detailOpen()}
          isEmpty={isEmpty()}
          listTestId="web-research-list"
          detailTestId="web-research-detail"
          empty={
            <p
              class="den-settings-hint den-settings-list-inbox__empty"
              data-testid="web-research-empty"
            >
              {WEB_RESEARCH_SETTINGS_COPY.chooseProvider}
            </p>
          }
          list={
            <For each={listEntries()}>
              {(entry) => (
                <Show
                  when={entry.kind === "direct"}
                  fallback={
                    <Show
                      when={entry.kind === "catalog" ? entry.meta : false}
                      keyed
                    >
                      {(meta) => (
                        <SettingsListRow
                          testId={`web-research-row-${meta.id}`}
                          leading={
                            <DenCheckbox
                              checked
                              data-testid={`web-research-enable-${meta.id}`}
                              onChange={(e) => {
                                if (!e.currentTarget.checked) {
                                  void removeProvider(meta);
                                }
                              }}
                            >
                              <span class="sr-only">
                                {WEB_RESEARCH_SETTINGS_COPY.providerLabel(
                                  meta.id as WebResearchProviderId,
                                )}
                              </span>
                            </DenCheckbox>
                          }
                          primary={WEB_RESEARCH_SETTINGS_COPY.providerLabel(
                            meta.id as WebResearchProviderId,
                          )}
                          secondary={WEB_RESEARCH_SETTINGS_COPY.providerHint(
                            meta.id as WebResearchProviderId,
                          )}
                          status={
                            <span
                              class="den-settings-provider-status"
                              data-configured={providerIsReady(meta)}
                            >
                              {providerStatusLabel(meta)}
                            </span>
                          }
                          onSelect={() => selectRow(meta.id)}
                        />
                      )}
                    </Show>
                  }
                >
                  <SettingsListRow
                    testId={`web-research-row-${DIRECT_PROVIDER_ID}`}
                    leading={
                      <DenCheckbox
                        checked
                        data-testid="web-research-enable-direct"
                        onChange={(e) => {
                          if (!e.currentTarget.checked) {
                            props.onRemoveProvider(DIRECT_PROVIDER_ID);
                            if (selectedId() === DIRECT_PROVIDER_ID) {
                              setSelectedId(null);
                            }
                          }
                        }}
                      >
                        <span class="sr-only">
                          {props.status.direct.card.label}
                        </span>
                      </DenCheckbox>
                    }
                    primary={props.status.direct.card.label}
                    secondary={WEB_RESEARCH_SETTINGS_COPY.directHint}
                    status={
                      <span
                        class="den-settings-provider-status"
                        data-configured={props.status.direct.configured}
                      >
                        {directStatusLabel(props.status.direct.configured)}
                      </span>
                    }
                    onSelect={() => selectRow(DIRECT_PROVIDER_ID)}
                  />
                </Show>
              )}
            </For>
          }
          detail={
            <Show
              when={selectedId() === DIRECT_PROVIDER_ID}
              fallback={
                // Provider identity survives status refreshes.
                <ShowLatest
                  when={props.status.providers.find((p) => p.id === selectedId())}
                  by={(meta) => meta.id}
                >
                  {(meta) => (
                    <WebResearchProviderCard
                      meta={meta()}
                      draft={card(meta().id)}
                      extraFields={extraFields(meta())}
                      onPatch={(patch) => patchCard(meta().id, patch)}
                      onSaveCredential={() => void saveCredential(meta())}
                      onClearCredential={() => void clearCredential(meta())}
                      onSaveConfig={() => void saveConfig(meta())}
                      onTest={() => void testProvider(meta())}
                      onRemove={() => void removeProvider(meta())}
                    />
                  )}
                </ShowLatest>
              }
            >
              {renderDirectDetail()}
            </Show>
          }
        />
      </SettingsListPanel>
      {renderAddDialog()}
    </>
  );
}
