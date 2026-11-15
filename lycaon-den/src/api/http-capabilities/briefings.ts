import type { JsonRequester } from "../http.ts";
import type { BriefingRecord, BriefingRequest, BriefingSnapshot } from "../types.ts";

export interface BriefingsClient {
  /** Asks for a step, activity or worker briefing; answers with its snapshot. */
  requestBriefing(projectId: string, req: BriefingRequest): Promise<BriefingSnapshot>;
  getBriefingRecord(projectId: string, recordId: string): Promise<BriefingRecord>;
}

export function createBriefingsClient(j: JsonRequester): BriefingsClient {
  return {
    requestBriefing: (projectId, req) =>
      j(`/v1/projects/${projectId}/briefings`, { method: "POST", body: JSON.stringify(req) }),
    getBriefingRecord: (projectId, recordId) =>
      j(`/v1/projects/${projectId}/briefings/records/${encodeURIComponent(recordId)}`),
  };
}
