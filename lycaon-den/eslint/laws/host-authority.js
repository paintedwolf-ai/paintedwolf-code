import { COMPACTION_BANNER_OPEN, REJECTED, VERBATIM_HEAD_TAIL } from "../../src/chat/host-markers.generated.ts";

const SRC = "src/**/*.{ts,tsx}";
const DECISION_SURFACES = "src/{components,chat,notices,workflow,session,shell,blueprint,store,find,list}/**/*.{ts,tsx}";
const WORKFLOW_SURFACES = "src/{components,chat,workflow}/**/*.{ts,tsx}";

function escaped(text) {
  return text.replace(/[.*+?^${}()|[\]\\/]/g, "\\$&");
}

// Markers short enough to occur in ordinary text are not scanned for.
const SCANNED_MARKERS = [VERBATIM_HEAD_TAIL, COMPACTION_BANNER_OPEN, REJECTED].map(escaped).join("|");

/** Den presents decisions the host made; it reads typed fields, never prose or policy. */
export default [
  {
    name: "host-markers-one-spelling",
    message: "Read host markers from chat/host-markers.generated.ts; a second spelling keeps matching text nobody writes.",
    files: [SRC],
    ignores: ["src/chat/host-markers.generated.ts", "src/**/*-fixtures.{ts,tsx}"],
    syntax: [
      `Literal[value=/${SCANNED_MARKERS}/]`,
      `TemplateElement[value.raw=/${SCANNED_MARKERS}/]`,
      `Literal[regex.pattern=/${SCANNED_MARKERS}/]`,
    ],
    seed: { file: "src/chat/tool/tool-part-model.ts", code: `export const rejected = (text: string) => text.startsWith("${REJECTED}");` },
  },
  {
    name: "markdown-reads-no-rejection-prose",
    message: "Markdown display decides nothing from rejection prose; the host field carries the outcome.",
    files: ["src/chat/markdown/markdown-output.ts"],
    syntax: [
      "Literal[value=/\\bREJECTED\\b/]",
      "TemplateElement[value.raw=/\\bREJECTED\\b/]",
      "Literal[regex.pattern=/REJECTED/]",
    ],
    seed: { code: `export const rejected = (text: string) => text.includes("REJECTED");` },
  },
  {
    name: "workflow-start-is-host-gated",
    message: "Den does not branch on the workflow-start approval code; the host reports pending_workflow_start.",
    files: [SRC],
    syntax: ["Literal[value='WORKFLOW_START_REQUIRES_HUMAN_APPROVAL']"],
    seed: { file: "src/chat/tool/tool-part-model.ts", code: `export const code = "WORKFLOW_START_REQUIRES_HUMAN_APPROVAL";` },
  },
  {
    name: "no-branching-on-hint-codes",
    message: "Decision surfaces read host outcome fields, not rejection codes or message text.",
    files: [DECISION_SURFACES],
    syntax: [
      "SwitchStatement[discriminant.property.name='code']",
      "SwitchStatement[discriminant.expression.property.name='code']",
      "SwitchCase > Literal[value=/^[A-Z][A-Z0-9_]{6,}$/]",
    ],
    seed: {
      file: "src/chat/tool/tool-part-model.ts",
      code: `export function f(result: { code: string }) { switch (result.code) { case "PLAN_REQUIRED": return 1; default: return 0; } }`,
    },
  },
  {
    name: "no-decisions-from-message-text",
    message: "Decision surfaces read host outcome fields, not message text.",
    files: [DECISION_SURFACES],
    ignores: ["src/chat/transcript/projection/transcript-items.ts", "src/chat/markdown/markdown-output*.ts"],
    syntax: ["CallExpression[callee.property.name='includes'][callee.object.property.name='content'][callee.object.object.name=/^(message|msg)$/]"],
    seed: {
      file: "src/chat/tool/tool-part-model.ts",
      code: `export const approved = (message: { content: string }) => message.content.includes("approved");`,
    },
  },
  {
    name: "spawn-policy-stays-on-the-host",
    message: "The host decides which agents may spawn; Den does not read the allowlist.",
    files: [WORKFLOW_SURFACES],
    ignores: ["src/**/types.ts"],
    syntax: [
      "MemberExpression[property.name='allowed_agents']",
      "Property[key.name='allowed_agents']",
      "Literal[value=/allowed_agents|DISALLOWED_AGENT|DefaultImplementSpawnAgents/]",
      "Identifier[name=/DISALLOWED_AGENT|DefaultImplementSpawnAgents/]",
    ],
    seed: {
      file: "src/chat/worker/workers-model.ts",
      code: `export const canSpawn = (policy: { allowed_agents: string[] }, agent: string) => policy.allowed_agents.includes(agent);`,
    },
  },
  {
    name: "transcript-visibility-is-a-wire-field",
    message: "Transcript visibility comes from the wire field, not a content prefix.",
    files: ["src/chat/transcript/projection/transcript-items.ts"],
    syntax: [
      "Literal[value=/\\[host:/]",
      "TemplateElement[value.raw=/\\[host:/]",
      "Literal[regex.pattern=/host:/]",
      "Identifier[name='isHostInjectedUserMessage']",
    ],
    seed: { code: `export const hidden = (content: string) => content.startsWith("[host:");` },
  },
  {
    name: "worker-identity-is-the-dispatch-record",
    message: "Bind a task to its worker through tool_result.dispatch job and session ids only.",
    files: [SRC],
    ignores: ["src/**/types.ts"],
    syntax: [
      "MemberExpression[object.property.name='tool_result'][property.name=/^(worker_id|child_session_id)$/]",
      "MemberExpression[object.name='tool_result'][property.name=/^(worker_id|child_session_id)$/]",
      "MemberExpression[property.name='state'][object.property.name='dispatch']",
      "MemberExpression[property.name='state'][object.name='dispatch']",
    ],
    seed: {
      file: "src/chat/worker/workers-model.ts",
      code: `export const open = (part: { tool_result: { dispatch: { state: string } } }) => part.tool_result.dispatch.state === "open";`,
    },
  },
  {
    name: "workers-match-by-wire-identity",
    message: "A task matches its worker by wire job or session id, never by agent type or prompt text.",
    files: ["src/chat/worker/workers-model.ts"],
    syntax: [
      "CallExpression[callee.name=/^(subagentType|taskDescription)$/][arguments.0.name='part']",
      "CallExpression[callee.property.name='includes'][callee.object.name='prompt']",
    ],
    seed: { code: `export const matches = (prompt: string, brief: string) => prompt.includes(brief);` },
  },
  {
    name: "app-state-holds-no-workflow-gates",
    message: "App state mirrors host workflow state; it keeps no gate flag of its own.",
    files: ["src/store/{app-state,session-activity-actions,session-coordination-actions,git-cache-actions}.ts"],
    syntax: [
      "Identifier[name=/planApproved|gateBlocked|spawnAllowlist|handoffReady/]",
      "Literal[value=/planApproved|gateBlocked|spawnAllowlist|handoffReady/]",
    ],
    seed: { file: "src/store/app-state.ts", code: `export const planApproved = false;` },
  },
  {
    name: "transport-goes-through-the-api-client",
    message: "Only src/api and the backend health probe call fetch.",
    files: [SRC],
    ignores: ["src/api/**", "src/platform/connection/backend.ts"],
    syntax: ["CallExpression[callee.name='fetch']", "CallExpression[callee.property.name='fetch']"],
    seed: { file: "src/chat/tool/tool-part-model.ts", code: `export const load = () => fetch("/v1/sessions");` },
  },
  {
    name: "recovery-reads-no-prose",
    message: "Recovery follows idle_disposition and typed fields; it matches no prose.",
    files: ["src/chat/recovery/**/*.ts"],
    syntax: [
      "CallExpression[callee.property.name='includes'][callee.object.callee.property.name='toLowerCase']",
      "CallExpression[callee.property.name='startsWith'][arguments.0.value=/^(stopped|error|undo)/]",
      "Literal[regex.pattern=/stopped|failed|error/][regex.flags=/i/]",
    ],
    seed: {
      file: "src/chat/recovery/turn-outcome.ts",
      code: `export const stopped = (text: string) => text.toLowerCase().includes("stopped");`,
    },
  },
  {
    name: "prose-navigation-probes-no-filesystem",
    message: "Prose navigation resolves against the host index; it never probes paths from the client.",
    files: ["src/chat/markdown/prose-path-opens.ts", "src/components/transcript/AssistantProseBody.tsx"],
    syntax: [
      "Identifier[name=/pathKind|path_kind|InventoryProjectSource|confirmJail/]",
      "Literal[value=/pathKind|path_kind/]",
    ],
    seed: { file: "src/chat/markdown/prose-path-opens.ts", code: `export const pathKind = "file";` },
  },
];
