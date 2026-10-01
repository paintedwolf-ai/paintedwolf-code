import { createEffect, createSignal, on, onCleanup } from "solid-js";
import type { McpProvider, McpToolInfo } from "../../../api/types.ts";

import { type LoadState, loadFailed, loaded, unloaded } from "../../../store/load-state.ts";
import type { McpSettingsScope } from "./McpSettingsPanel.tsx";

type McpProviderToolsOptions = Pick<McpSettingsScope, "currentClient" | "projectId" | "providers" | "providerWriter" | "catchNotice" | "setOperationError">;

export function createMcpProviderTools({ currentClient, projectId, providers, providerWriter, catchNotice, setOperationError }: McpProviderToolsOptions) {
  const [toolsOpenId, setToolsOpenId] = createSignal<string | undefined>();
  // Listing failures remain distinct from an empty tool list.
  const [toolsById, setToolsById] = createSignal<Record<string, LoadState<McpToolInfo[]>>>(
    {},
  );
  const [toolsLoadingId, setToolsLoadingId] = createSignal<string | undefined>();
  let toolsGeneration = 0;
  onCleanup(() => { toolsGeneration += 1; });
  const [resyncingId, setResyncingId] = createSignal<string | undefined>();
  const loadTools = async (provider: McpProvider) => {
    const generation = ++toolsGeneration;
    setOperationError(undefined);
    if (!provider.enabled) {
      setToolsLoadingId(undefined);
      setToolsById((prev) => ({ ...prev, [provider.id]: unloaded<McpToolInfo[]>() }));
      return;
    }
    setToolsLoadingId(provider.id);
    try {
      const tools = await currentClient().listMcpProviderTools(
        provider.id,
        projectId(),
      );
      if (generation !== toolsGeneration) return;
      setToolsById((prev) => ({ ...prev, [provider.id]: loaded(tools) }));
    } catch (err) {
      if (generation !== toolsGeneration) return;
      setToolsById((prev) => ({ ...prev, [provider.id]: loadFailed(err) }));
      setOperationError(catchNotice(err));
    } finally {
      if (generation === toolsGeneration) setToolsLoadingId(undefined);
    }
  };

  // Provider changes invalidate cached tool definitions even when reconnecting fails.
  const observeProviderTools = () => createEffect(on(
    () => [currentClient(), projectId(), providers()] as const,
    (next, previous) => {
      toolsGeneration += 1;
      setToolsById({});
      setToolsLoadingId(undefined);
      if (previous && (next[0] !== previous[0] || next[1] !== previous[1])) {
        setToolsOpenId(undefined);
        return;
      }
      const opened = next[2].find((provider) => provider.id === toolsOpenId());
      if (opened) void loadTools(opened);
    },
    { defer: true },
  ));

  const toggleTools = async (provider: McpProvider) => {
    if (toolsOpenId() === provider.id) {
      setToolsOpenId(undefined);
      return;
    }
    setToolsOpenId(provider.id);
    const known = toolsById()[provider.id];
    // Reopen retries a failed listing; a settled one serves from memory.
    if (known === undefined || known.state === "error") {
      await loadTools(provider);
    }
  };

  const resyncProvider = async (provider: McpProvider) => {
    const setProviders = providerWriter();
    setResyncingId(provider.id);
    setOperationError(undefined);
    try {
      const updated = await currentClient().refreshMcpProvider(
        provider.id,
        projectId(),
      );
      setProviders((prev) =>
        prev.map((s) => (s.id === updated.id ? updated : s)),
      );
    } catch (err) {
      setOperationError(catchNotice(err));
    } finally {
      setResyncingId(undefined);
    }
  };

  return { toolsOpenId, setToolsOpenId, toolsById, toolsLoadingId, resyncingId, observeProviderTools, toggleTools, resyncProvider };
}
