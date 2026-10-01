import type { LycaonClient } from "../../api/client.ts";
import type { CoordinatorRunContext } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";

const contextRefreshes = new WeakMap<AppStore, object>();

export async function refreshCoordinatorContext(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
): Promise<CoordinatorRunContext | undefined> {
  if (!sessionId.trim()) return undefined;
  const epoch = appStore.state.sessionViewEpoch;
  const workflowEpoch = appStore.state.workflowEventEpoch;
  const request = {};
  if (appStore.state.currentSession?.id?.trim() === sessionId.trim()) contextRefreshes.set(appStore, request);
  const ctx = await client.getCoordinatorContext(sessionId);
  if (contextRefreshes.get(appStore) !== request || appStore.state.workflowEventEpoch !== workflowEpoch) return undefined;
  appStore.actions.setCoordinatorRunContext(sessionId, epoch, ctx);
  return ctx;
}
