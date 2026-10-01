import { createEffect, createSignal } from "solid-js";
import type { McpCheckRow, McpProvider, UpdateMcpProviderRequest } from "../../../api/types.ts";
import type { AppNotice } from "../../../notices/notice-model.ts";
import { catalogProviders, providersForScope } from "../../../settings/mcp/mcp-provider-model.ts";
import { MCP_SETTINGS_COPY } from "../../../settings/mcp/mcp-settings-copy.ts";

import { confirmDestructive } from "../../../platform/interaction/confirm-dialog.ts";
import type { McpSettingsScope } from "./McpSettingsPanel.tsx";

export const projectDeviceManaged = (provider: McpProvider) =>
(provider.transport === "stdio" ||
  Boolean(provider.token_present) ||
  Boolean(provider.headers_present));

type McpProviderActionsOptions = Pick<McpSettingsScope, "currentClient" | "projectId" | "providers" | "providerWriter" | "reloadProviders" | "catchNotice" | "setOperationError">;

export function createMcpProviderActions({ currentClient, projectId, providers, providerWriter, reloadProviders, catchNotice, setOperationError }: McpProviderActionsOptions) {
  const [checkRows, setCheckRows] = createSignal<McpCheckRow[] | undefined>();
  const [checkRunning, setCheckRunning] = createSignal(false);
  const [checkError, setCheckError] = createSignal<AppNotice | undefined>();
  const [togglingId, setTogglingId] = createSignal<string | undefined>();
  const [busyOverride, setBusyOverride] = createSignal(false);
  const [projectOverrideDraft, setProjectOverrideDraft] = createSignal<
    boolean | undefined
  >();
  const projectOverridePersisted = () =>
    providers().some((s) => s.source === "override");

  const projectOverrideEnabled = () =>
    projectOverrideDraft() ?? projectOverridePersisted();

  const observeOverride = () => createEffect(() => {
    const draft = projectOverrideDraft();
    if (draft !== undefined && draft === projectOverridePersisted()) {
      setProjectOverrideDraft(undefined);
    }
  });

  const applyProviderUpdate = async (
    provider: McpProvider,
    body: UpdateMcpProviderRequest,
  ) => {
    const setProviders = providerWriter();
    setTogglingId(provider.id);
    setOperationError(undefined);
    try {
      const updated = await currentClient().updateMcpProvider(
        provider.id,
        body,
        projectId(),
      );
      setProviders((prev) =>
        prev.map((s) => (s.id === updated.id ? updated : s)),
      );
      return updated;
    } catch (err) {
      setOperationError(catchNotice(err));
      return undefined;
    } finally {
      setTogglingId(undefined);
    }
  };

  const toggleEnabled = async (provider: McpProvider, enabled: boolean) => {
    await applyProviderUpdate(provider, { enabled });
  };

  const toggleAlwaysLoad = async (provider: McpProvider, always: boolean) => {
    await applyProviderUpdate(provider, {
      tool_loading: always ? "always" : "auto",
    });
  };

  const applyProjectOverride = async (enabled: boolean) => {
    if (enabled === projectOverrideEnabled()) return;
    setProjectOverrideDraft(enabled);
    setBusyOverride(true);
    setOperationError(undefined);
    try {
      const list = providersForScope(catalogProviders(providers()), true);
      if (!enabled) {
        for (const provider of list.filter((s) => s.source === "override")) {
          await currentClient().deleteMcpProvider(
            provider.id,
            projectId(),
          );
        }
      } else {
        for (const provider of list) {
          if (provider.enabled && projectDeviceManaged(provider)) continue;
          await currentClient().updateMcpProvider(
            provider.id,
            { enabled: provider.enabled },
            projectId(),
          );
        }
      }
      await reloadProviders();
    } catch (err) {
      setOperationError(catchNotice(err));
      setProjectOverrideDraft(undefined);
    } finally {
      setBusyOverride(false);
    }
  };

  const testConnection = async () => {
    setCheckRunning(true);
    setCheckError(undefined);
    try {
      const rows = await currentClient().checkMcpProviders(projectId());
      setCheckRows(rows);
      await reloadProviders();
    } catch (err) {
      setCheckError(catchNotice(err));
    } finally {
      setCheckRunning(false);
    }
  };

  const deleteProvider = async (provider: McpProvider) => {
    const confirmed = await confirmDestructive({
      title: MCP_SETTINGS_COPY.deleteTitle,
      message: MCP_SETTINGS_COPY.deleteConfirm,
      okLabel: MCP_SETTINGS_COPY.delete,
      cancelLabel: MCP_SETTINGS_COPY.cancel,
      destructive: true,
    });
    if (!confirmed) return;
    setTogglingId(provider.id);
    setOperationError(undefined);
    try {
      await currentClient().deleteMcpProvider(provider.id, projectId());
      await reloadProviders();
    } catch (err) {
      setOperationError(catchNotice(err));
    } finally {
      setTogglingId(undefined);
    }
  };

  return { checkRows, setCheckRows, checkRunning, checkError, togglingId, busyOverride, projectOverrideEnabled, observeOverride, toggleEnabled, toggleAlwaysLoad, applyProjectOverride, testConnection, deleteProvider };
}
