import type { LycaonClient } from "../../api/client.ts";
import type { ProgressDigest } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";

export async function refreshProgress(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  minRevision = 0,
): Promise<ProgressDigest | undefined> {
  if (!sessionId.trim()) return undefined;
  const epoch = appStore.state.sessionViewEpoch;
  const digest = await client.getSessionProgress(sessionId);
  if (minRevision > 0 && digest.revision < minRevision) {
    return undefined;
  }
  appStore.actions.setProgress(sessionId, epoch, digest);
  return digest;
}
