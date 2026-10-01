import type { LycaonClient } from "../api/client.ts";
import { getLycaonClient } from "../platform/connection/app-connection.ts";
import type { AppStore } from "../store/app-state-model.ts";

/** Sidecar connection state shared by settings and project configuration views. */
export function useSettingsBackend(appStore: AppStore) {
  const client = (): LycaonClient | null => {
    // Track connection changes.
    appStore.state.sidecarStatus;
    return getLycaonClient();
  };
  const backendConnecting = () => {
    const status = appStore.state.sidecarStatus;
    return (
      client() === null &&
      (status === "connecting" || status === "reconnecting")
    );
  };

  return { backendConnecting, client };
}
