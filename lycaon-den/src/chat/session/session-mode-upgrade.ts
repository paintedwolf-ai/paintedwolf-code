import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import { beginSessionSnapshotRead, refreshSessionSnapshot } from "../../api/session-snapshot-refresh.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { confirmSessionModeAdmission, sessionModeView, setSessionModeUpgrade } from "./session-mode-state.ts";

function isConfirmedRefusal(error: unknown): boolean {
  return error instanceof LycaonApiError &&
    error.status !== undefined && error.status >= 400 && error.status < 500;
}

async function requestSessionModeUpgrade(appStore: AppStore, client: LycaonClient, sessionId: string): Promise<boolean> {
  const view = sessionModeView(appStore, sessionId);
  if (view.mode === "agent") return true;
  if (view.mode !== "ask" || !view.started) return false;

  const connection = beginSessionSnapshotRead(appStore, sessionId);
  setSessionModeUpgrade(appStore, sessionId, "sending");
  try {
    const session = await client.updateSession(sessionId, { mode: "agent" });
    if (!connection.isSameBackend()) return false;
    if (session.id === sessionId && session.started && session.mode === "agent") {
      // A successful upgrade remains authoritative across later revisions.
      confirmSessionModeAdmission(appStore, sessionId, session.mode);
      return true;
    }
  } catch (error) {
    if (!connection.isSameBackend()) return false;
    if (sessionModeView(appStore, sessionId).mode === "agent") return true;
    if (isConfirmedRefusal(error)) {
      setSessionModeUpgrade(appStore, sessionId, undefined);
      throw error;
    }
  } finally {
    connection.finish();
  }

  setSessionModeUpgrade(appStore, sessionId, "reconciling");
  await refreshSessionSnapshot(appStore, client, sessionId).catch(() => undefined);
  return sessionModeView(appStore, sessionId).mode === "agent";
}

type Upgrade = { client: LycaonClient; response: Promise<boolean> };
const upgradesByStore = new WeakMap<AppStore, Map<string, Upgrade>>();

export function upgradeSessionMode(appStore: AppStore, client: LycaonClient, sessionId: string): Promise<boolean> {
  let upgrades = upgradesByStore.get(appStore);
  if (!upgrades) upgradesByStore.set(appStore, upgrades = new Map());
  const active = upgrades.get(sessionId);
  if (active?.client === client) return active.response;
  const response = requestSessionModeUpgrade(appStore, client, sessionId).finally(() => {
    if (upgrades.get(sessionId)?.response === response) upgrades.delete(sessionId);
  });
  upgrades.set(sessionId, { client, response });
  return response;
}
