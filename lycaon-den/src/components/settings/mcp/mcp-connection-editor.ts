import { createSignal } from "solid-js";
import type { McpProvider } from "../../../api/types.ts";
import type { AppNotice } from "../../../notices/notice-model.ts";
import { providerLocksCredentialWire } from "../../../settings/mcp/mcp-provider-model.ts";

import { classifyMcpHttpUrl } from "../../../settings/mcp/mcp-http-url.ts";

import { type ConnectionDraft, emptyConnectionDraft, draftFromProvider, connectionBodyFromDraft } from "./mcp-connection-draft.ts";
import type { McpSettingsScope } from "./McpSettingsPanel.tsx";

type McpConnectionEditorOptions = Pick<McpSettingsScope, "currentClient" | "projectId" | "projectLocal" | "providerWriter" | "catchNotice">;

export function createMcpConnectionEditor({ currentClient, projectId, projectLocal, providerWriter, catchNotice }: McpConnectionEditorOptions) {
  const [connectionDraft, setConnectionDraft] = createSignal<ConnectionDraft>(
    emptyConnectionDraft(),
  );
  const [connectionBusy, setConnectionBusy] = createSignal(false);
  const [connectionError, setConnectionError] = createSignal<
    AppNotice | undefined
  >();
  const saveConnection = async (provider: McpProvider) => {
    const setProviders = providerWriter();
    const draft = connectionDraft();
    let httpURL: string | undefined;
    if (draft.transport === "http") {
      const classified = classifyMcpHttpUrl(draft.url, projectLocal());
      if (!classified.ok) {
        return;
      }
      httpURL = classified.url;
    }
    setConnectionBusy(true);
    setConnectionError(undefined);
    try {
      const updated = await currentClient().updateMcpProvider(
        provider.id,
        connectionBodyFromDraft(
          draft,
          httpURL,
          providerLocksCredentialWire(provider),
        ),
        projectId(),
      );
      setProviders((prev) =>
        prev.map((s) => (s.id === updated.id ? updated : s)),
      );
      setConnectionDraft(draftFromProvider(updated));
    } catch (err) {
      setConnectionError(catchNotice(err));
    } finally {
      setConnectionBusy(false);
    }
  };

  return { connectionDraft, setConnectionDraft, connectionBusy, connectionError, setConnectionError, saveConnection };
}
