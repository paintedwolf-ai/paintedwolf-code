import { randomUUID } from "node:crypto";
import { expect, type APIRequestContext } from "@playwright/test";
import { apiConfig } from "./helpers.ts";

export function manualHarnessTools(request: APIRequestContext, sessionId: string) {
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  return {
    async invoke(name: string, args: Record<string, unknown>) {
      const response = await request.get(`${apiUrl}/harness/llm/pending?wait=30000&session_id=${sessionId}`, { headers });
      expect(response.ok(), await response.text()).toBeTruthy();
      const pending = await response.json() as {
        pending: boolean; id: string; session_id: string; tools: string[];
      };
      expect(pending.pending).toBe(true);
      expect(pending.session_id).toBe(sessionId);
      expect(pending.tools).toContain(name);
      const id = randomUUID();
      const reply = await request.post(`${apiUrl}/harness/llm/respond`, {
        headers, data: { id: pending.id, content: "", tool_calls: [{ id, name, args }] },
      });
      expect(reply.ok(), await reply.text()).toBeTruthy();
      return id;
    },
    // An exact tool name loads that tool without ranking, so each name is one call.
    async loadTools(...names: string[]) {
      for (const need of names) await this.completed(await this.invoke("request_tools", { need }));
    },
    async completed(callId: string) {
      let result: { outcome?: string; content: string } | undefined;
      await expect.poll(async () => {
        const response = await request.get(`${apiUrl}/v1/sessions/${sessionId}/messages`, { headers });
        expect(response.ok(), await response.text()).toBeTruthy();
        const body = await response.json() as {
          messages: Array<{ tool_result?: { tool_call_id?: string; outcome?: string; content: string } }>;
        };
        result = body.messages.find((message) => message.tool_result?.tool_call_id === callId)?.tool_result;
        return result;
      }, { timeout: 45_000 }).toBeTruthy();
      expect(result?.outcome, result?.content).toBe("completed");
      return result!.content;
    },
  };
}
