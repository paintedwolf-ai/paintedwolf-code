import type { LycaonClient } from "../../api/client.ts";
import type { WebResearchIndexStatus } from "../../api/types.ts";

/**
 * Clear the personal web index through POST /v1/local-data/clear (same host
 * path as the web_index local-data bucket), then return fresh index status for
 * the Web research panel.
 */
export async function clearWebIndexViaLocalData(
  client: LycaonClient,
): Promise<WebResearchIndexStatus> {
  await client.clearLocalData({ buckets: ["web_index"] });
  return client.getWebResearchIndex();
}
