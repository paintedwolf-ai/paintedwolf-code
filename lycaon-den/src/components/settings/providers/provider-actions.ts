import { createEffect } from "solid-js";
import { createStore } from "solid-js/store";
import type { ProviderMeta } from "../../../api/types.ts";
import { nextNumberedProviderLabel, providerKindIsCustom, uniqueProviderId } from "../../../settings/providers/models-editor-model.ts";
import { kindOffersCredentialChoice } from "../../../settings/providers/provider-credential-modes.ts";
import { MODELS_SETTINGS_COPY } from "../../../settings/providers/models-settings-copy.ts";
import { providerEndpointError } from "../../../settings/providers/cloudflare-endpoint.ts";
import { type ProviderCardDraft, refreshFailureCopy, emptyCardUI, emptyForm, endpointStyle, formatConfiguredModels, parseConfiguredModels } from "./provider-form-model.ts";
import type { LycaonClient } from "../../../api/client.ts";
import type { ProviderKindTemplate } from "../../../api/types.ts";
import type { SettingsStore } from "../../../store/settings-store.ts";
import type { NewProviderForm } from "./provider-form-model.ts";

type ProviderActionsOptions = {
  client: () => LycaonClient;
  providers: () => readonly ProviderMeta[];
  settingsStore: () => SettingsStore;
  form: () => NewProviderForm;
  setForm: (patch: Partial<NewProviderForm>) => unknown;
  kinds: () => readonly ProviderKindTemplate[];
  selectedTemplate: () => ProviderKindTemplate | undefined;
  existingIds: () => Set<string>;
  selectedId: () => string | null;
  setSelectedId: (id: string | null) => unknown;
  setShowAddForm: (visible: boolean) => unknown;
  notifyAddOpenChange: (visible: boolean) => unknown;
  removeProviderFromSettings: (id: string) => void | Promise<void>;
};

export function createProviderActions(options: ProviderActionsOptions) {
  const { client, providers, settingsStore, form, setForm, kinds, selectedTemplate, existingIds,
    selectedId, setSelectedId, setShowAddForm, notifyAddOpenChange, removeProviderFromSettings } = options;
  const [cardUI, setCardUI] = createStore<Record<string, ProviderCardDraft>>({});
  const validationVersions = new Map<string, number>();
  const card = (id: string): ProviderCardDraft => cardUI[id] ?? emptyCardUI();

  const patchCard = (id: string, patch: Partial<ProviderCardDraft>) => {
    if (cardUI[id] === undefined) {
      setCardUI(id, { ...emptyCardUI(), ...patch });
      return;
    }
    for (const [key, value] of Object.entries(patch) as [
      keyof ProviderCardDraft,
      ProviderCardDraft[keyof ProviderCardDraft],
    ][]) {
      setCardUI(id, key, value);
    }
  };

  const invalidateValidation = (id: string) => {
    const version = (validationVersions.get(id) ?? 0) + 1;
    validationVersions.set(id, version);
    patchCard(id, { testing: false, refreshing: false, testMessage: undefined, refreshFailure: undefined });
    return version;
  };

  const reportError = (err: unknown) => {
    settingsStore().actions.setError(
      err instanceof Error ? err.message : String(err),
    );
  };

  const refreshProviders = async () => {
    const list = await client().listProviders();
    settingsStore().actions.setProviders(list);
  };

  const onKindChange = (kind: string) => {
    const tmpl = kinds().find((k) => k.kind === kind);
    setForm({
      kind,
      baseUrl: tmpl?.base_url ?? "",
      models: "",
      apiKey: "",
      error: undefined,
      // Credential-choice kinds start in stored-key mode.
      requireApiKey: kindOffersCredentialChoice(tmpl),
    });
  };

  const addProvider = async () => {
    const baseUrl = form().baseUrl.trim();
    const tmpl = selectedTemplate();
    const style = endpointStyle(tmpl?.endpoint_style);
    if (!form().kind || (style !== "derived" && !baseUrl)) return;
    if (providerEndpointError(form().kind, baseUrl)) return;
    const typedLabel = form().label.trim();
    const ids = existingIds();
    const apiKey = form().apiKey.trim();

    let id: string;
    let label: string;
    if (typedLabel) {
      id = uniqueProviderId(typedLabel, ids);
      label = typedLabel;
    } else {
      label = nextNumberedProviderLabel(tmpl?.label || form().kind, providers());
      id = uniqueProviderId(label, ids);
    }
    const requireApiKey =
      providerKindIsCustom(form().kind) || kindOffersCredentialChoice(tmpl)
        ? form().requireApiKey
        : (tmpl?.requires_api_key ?? false);
    setForm({ busy: true, error: undefined });
    try {
      await client().createProvider({
        id,
        ...(style === "derived" ? {} : { base_url: baseUrl }),
        kind: form().kind,
        label,
        requires_api_key: requireApiKey,
        ...(form().kind === "azure"
          ? { models: parseConfiguredModels(form().models) }
          : {}),
      });
      if (apiKey) {
        await client().replaceProviderCredential(id, apiKey);
      }
      await refreshProviders();
      setForm(emptyForm());
      setShowAddForm(false);
      notifyAddOpenChange?.(false);
      setSelectedId(id);
    } catch (err) {
      setForm({ busy: false, error: err instanceof Error ? err.message : String(err) });
    }
  };

  const saveEndpoint = async (provider: ProviderMeta) => {
    const baseUrl = card(provider.id).baseUrl.trim();
    if (!baseUrl) return;
    if (providerEndpointError(provider.kind, baseUrl)) return;
    patchCard(provider.id, { savingEndpoint: true });
    try {
      await client().updateProvider(provider.id, {
        base_url: baseUrl,
      });
      invalidateValidation(provider.id);
      patchCard(provider.id, { savingEndpoint: false, baseUrl });
      await refreshProviders();
    } catch (err) {
      patchCard(provider.id, { savingEndpoint: false });
      reportError(err);
    }
  };

  const saveModels = async (provider: ProviderMeta) => {
    const models = parseConfiguredModels(card(provider.id).models);
    patchCard(provider.id, { savingModels: true });
    try {
      await client().updateProvider(provider.id, { models });
      invalidateValidation(provider.id);
      patchCard(provider.id, { savingModels: false });
      await refreshProviders();
    } catch (err) {
      patchCard(provider.id, { savingModels: false });
      reportError(err);
    }
  };

  /** Switching modes does not delete stored credentials. */
  const setCredentialMode = async (
    provider: ProviderMeta,
    requireApiKey: boolean,
  ) => {
    if (provider.requires_api_key === requireApiKey) return;
    try {
      await client().updateProvider(provider.id, {
        requires_api_key: requireApiKey,
      });
      invalidateValidation(provider.id);
      await refreshProviders();
    } catch (err) {
      reportError(err);
    }
  };

  /** The host binds the decision to the destination it resolves on save. */
  const setSecretScreenTrust = async (
    provider: ProviderMeta,
    trusted: boolean,
  ) => {
    if ((provider.secret_screen_trusted ?? false) === trusted) return;
    patchCard(provider.id, { savingSecretTrust: true });
    try {
      await client().updateProvider(provider.id, {
        secret_screen_trusted: trusted,
      });
      await refreshProviders();
    } catch (err) {
      reportError(err);
    } finally {
      patchCard(provider.id, { savingSecretTrust: false });
    }
  };

  const renameLabel = async (provider: ProviderMeta, label: string) => {
    const next = label.trim();
    if (!next) return;
    try {
      await client().updateProvider(provider.id, {
        label: next,
      });
      await refreshProviders();
    } catch (err) {
      reportError(err);
    }
  };

  const saveCredential = async (provider: ProviderMeta) => {
    const key = card(provider.id).apiKey.trim();
    if (!key) return;
    try {
      await client().replaceProviderCredential(provider.id, key);
      invalidateValidation(provider.id);
      patchCard(provider.id, { apiKey: "" });
      await refreshProviders();
    } catch (err) {
      reportError(err);
    }
  };

  const clearCredential = async (provider: ProviderMeta) => {
    try {
      await client().deleteProviderCredential(provider.id);
      invalidateValidation(provider.id);
      await refreshProviders();
    } catch (err) {
      reportError(err);
    }
  };

  const testProvider = async (provider: ProviderMeta) => {
    const version = invalidateValidation(provider.id);
    patchCard(provider.id, { testing: true, testMessage: undefined });
    try {
      const res = await client().testProvider(provider.id);
      if (validationVersions.get(provider.id) !== version) return;
      patchCard(provider.id, {
        testing: false,
        testMessage: res.ok
          ? `${MODELS_SETTINGS_COPY.testConnectionPassed}${res.latency_ms != null ? ` (${res.latency_ms} ms)` : ""}`
          : res.message ?? "Test failed",
      });
    } catch (err) {
      if (validationVersions.get(provider.id) !== version) return;
      patchCard(provider.id, {
        testing: false,
        testMessage: err instanceof Error ? err.message : String(err),
      });
    }
  };

  const refreshModels = async (provider: ProviderMeta) => {
    const version = invalidateValidation(provider.id);
    patchCard(provider.id, {
      refreshing: true,
      testMessage: undefined,
      refreshFailure: undefined,
    });
    try {
      const updated = await client().refreshProviderModels(provider.id);
      if (validationVersions.get(provider.id) !== version) return;
      settingsStore().actions.setProviders(
        providers().map((p) => (p.id === updated.id ? updated : p)),
      );
      patchCard(provider.id, {
        refreshing: false,
        testMessage: MODELS_SETTINGS_COPY.modelsFound(updated.models.length),
      });
    } catch (err) {
      if (validationVersions.get(provider.id) !== version) return;
      // Keep the last model list when refresh fails.
      patchCard(provider.id, {
        refreshing: false,
        refreshFailure: refreshFailureCopy(err),
      });
    }
  };

  const toggleModelList = async (provider: ProviderMeta) => {
    const expanded = !card(provider.id).expanded;
    patchCard(provider.id, { expanded });
    if (expanded) {
      await refreshProviders();
    }
  };

  const removeProvider = async (provider: ProviderMeta) => {
    try {
      await removeProviderFromSettings(provider.id);
      await client().deleteProvider(provider.id);
      patchCard(provider.id, { expanded: false, testMessage: undefined });
      if (selectedId() === provider.id) {
        setSelectedId(null);
      }
      await refreshProviders();
    } catch (err) {
      reportError(err);
    }
  };

  const observeProviders = () => {
    createEffect(() => {
      for (const provider of providers()) {
        const ui = cardUI[provider.id];
        if (ui === undefined) {
          setCardUI(provider.id, {
            ...emptyCardUI(),
            baseUrl: provider.base_url ?? "",
            models: formatConfiguredModels(provider),
          });
          continue;
        }
        if (!ui.baseUrl.trim() && provider.base_url) {
          setCardUI(provider.id, "baseUrl", provider.base_url);
        }
      }
    });

  };
  return { card, patchCard, onKindChange, addProvider, saveEndpoint, saveModels, setCredentialMode, setSecretScreenTrust, renameLabel, saveCredential, clearCredential, testProvider, refreshModels, toggleModelList, removeProvider, observeProviders };
}
