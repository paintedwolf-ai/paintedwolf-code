import type { LycaonClient } from "../../api/client.ts";
import { isApiErrorCode } from "../../api/http.ts";
import { beginSessionSnapshotRead, refreshSessionSnapshot } from "../../api/session-snapshot-refresh.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { confirmSessionModeAdmission, sessionModeView, setSessionModeUpgrade } from "./session-mode-state.ts";

const MODE_UPGRADE_REFUSALS = [
  "body_too_large",
  "forbidden",
  "invalid_json",
  "invalid_request",
  "rate_limited",
  "session_mode_locked",
  "session_not_found",
  "unauthorized",
  "unsupported_media_type",
] as const;

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
    if (isApiErrorCode(error, MODE_UPGRADE_REFUSALS)) {
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
