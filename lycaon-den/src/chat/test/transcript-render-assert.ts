import { expect } from "vitest";
import type { Message, WorkflowRun } from "../../api/types.ts";
import { buildChatTranscriptBlocks } from "../workflow/workflow-spans.ts";

/** Mirrors the ChatView spanBlocks memo — see transcript-render-totality.test.ts. */
export function assertChatTranscriptRenderable(
  messages: readonly Message[],
  workflowRuns: readonly WorkflowRun[],
  activeRun?: WorkflowRun,
): ReturnType<typeof buildChatTranscriptBlocks> {
  let blocks: ReturnType<typeof buildChatTranscriptBlocks>;
  expect(() => {
    blocks = buildChatTranscriptBlocks(messages, workflowRuns, activeRun);
  }).not.toThrow();
  return blocks!;
}
