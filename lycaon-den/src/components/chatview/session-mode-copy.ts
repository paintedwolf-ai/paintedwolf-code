export const NEW_CHAT_AGENT_INPUT_HINT = "What would you like to work on?";
export const ASK_INPUT_HINT = "Ask a question…";

export const SESSION_MODE_DESCRIPTION = {
  agent: "Can edit files and run commands.",
  ask: "Reads and answers. Can’t edit files or run commands.",
  upgrade: "Switches this chat to Agent. It can’t be switched back.",
} as const;

export const SESSION_MODE_UPGRADE_REFUSAL = "This chat could not switch to Agent.";

export const NEEDS_AGENT_BADGE = "Needs Agent";
export const WORKFLOW_NEEDS_AGENT = "needs Agent";
export const WORKFLOW_NEEDS_AGENT_NOTICE = "This workflow needs Agent.";
export const WORKFLOW_NEEDS_AGENT_HINT = "Switch to Agent to start this workflow…";
