import { createStore } from "solid-js/store";
import type { SettingsClient } from "../../../api/http-capabilities/settings.ts";
import type { ProviderMeta } from "../../../api/types.ts";

type Client = Pick<SettingsClient, "repairLocalGeneration" | "refreshProviderModels" | "listProviders">;
type Operation = { busy: boolean; error?: string };

export function createLocalGenerationActions(client: () => Client, publish: (providers: ProviderMeta[]) => void) {
  const [operations, setOperation] = createStore<Record<string, Operation>>({});
  const recover = async (provider: ProviderMeta, action: "retry" | "repair") => {
    if (operations[provider.id]?.busy) return;
    setOperation(provider.id, { busy: true, error: undefined });
    try {
      if (action === "repair") {
        const result = await client().repairLocalGeneration(provider.id);
        if (!result.ok) {
          setOperation(provider.id, "error", result.status.detail || "The model could not be repaired.");
        }
      } else {
        await client().refreshProviderModels(provider.id);
      }
      publish(await client().listProviders());
    } catch (error) {
      setOperation(provider.id, "error", error instanceof Error ? error.message : String(error));
      try {
        publish(await client().listProviders());
      } catch {
        // Preserve the action failure when status refresh also fails.
      }
    } finally {
      setOperation(provider.id, "busy", false);
    }
  };
  return {
    recover,
    busy: (id: string) => operations[id]?.busy ?? false,
    error: (id: string) => operations[id]?.error,
  };
}
