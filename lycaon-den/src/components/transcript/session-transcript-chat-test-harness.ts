import type { Message } from "../../api/types.ts";
import { saveVerboseMode } from "../../settings/system/debug-prefs.ts";
import { resetLivePreviewStoreForTests } from "../../chat/visual/preview-store.ts";
import { resetInvocationRecordingStoreForTests } from "../../chat/visual/invocation-recording-store.ts";

export async function resetSessionTranscriptChatTest(): Promise<void> {
  await saveVerboseMode(false);

  resetLivePreviewStoreForTests();
  resetInvocationRecordingStoreForTests();
}

export function dispatchPair(opts: {
  assistantId: string;
  tcId: string;
  trId: string;
  agentType: string;
  goal: string;
  jobId: string;
  ts?: [string, string];
	ord?: [number, number];
}): Message[] {
  const envelope = `<task job_id="${opts.jobId}" agent_type="${opts.agentType}" state="complete"/>`;
  return [
    {
      id: opts.assistantId,
      role: "assistant",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "",
      tool_calls: [
        {
          id: opts.tcId,
          name: "task",
          args: {
            agent_type: opts.agentType,
            brief: { goal: opts.goal, done_when: ["Return results."] },
          },
        },
      ],
      created_at: opts.ts?.[0] ?? "t",
		ord: opts.ord?.[0],
    },
    {
      id: opts.trId,
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content: envelope,
      tool_result: {
        content: envelope,
        dispatch: { worker_id: opts.jobId },
        tool: "task",
        tool_call_id: opts.tcId,
        assistant_message_id: opts.assistantId,
        tool_args: {
          agent_type: opts.agentType,
          brief: { goal: opts.goal, done_when: ["Return results."] },
        },
      },
      worker_summary: {
        worker_id: opts.jobId,
        child_session_id: `child-${opts.jobId}`,
        agent_type: opts.agentType,
        status: "complete",
        envelope: `<task job_id="${opts.jobId}" child_session_id="child-${opts.jobId}" agent_type="${opts.agentType}" state="complete"></task>`,
      },
      created_at: opts.ts?.[1] ?? "t",
		ord: opts.ord?.[1],
    },
  ];
}
