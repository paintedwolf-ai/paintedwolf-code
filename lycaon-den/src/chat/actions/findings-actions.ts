import type { LycaonClient } from "../../api/client.ts";
import type { FindingsDigest } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";

/** Read failures become store state instead of rejected promises. */
export async function refreshFindings(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  minRevision = 0,
): Promise<FindingsDigest | undefined> {
  if (!sessionId.trim()) return undefined;
  const epoch = appStore.state.sessionViewEpoch;
  const previous = appStore.state.findings;
  let digest: FindingsDigest;
  try {
    digest = await client.getSessionFindings(sessionId);
  } catch (err) {
    if (appStore.state.findings === previous) {
      appStore.actions.setFindingsLoadFailed(sessionId, epoch, err);
    }
    return undefined;
  }
  if (minRevision > 0 && digest.revision < minRevision) {
    return undefined;
  }
  appStore.actions.setFindings(sessionId, epoch, digest);
  return digest;
}
