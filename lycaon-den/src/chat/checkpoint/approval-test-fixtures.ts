import type {
  ApprovalGate,
  ApprovalOption,
  ApprovalPlan,
  ApprovalPlanPresentation,
  ApprovalRepeat,
  ApprovalSubject,
  ToolApprovalPayload,
} from "../../api/types.ts";

type PlanFixture = {
  tool?: string;
  command?: string;
  title?: string;
  impact?: string;
  stage?: ApprovalPlan["stage"];
  subject?: Partial<ApprovalSubject>;
  presentation?: Partial<ApprovalPlanPresentation>;
  options?: ApprovalOption[];
  reasons?: ApprovalGate[];
  recommendedOptionId?: string;
  joinedCount?: number;
  repeat?: ApprovalRepeat;
  elevatedEffects?: ApprovalPlan["elevated_effects"];
};

export function approvalOptionFixture(
  overrides: Partial<ApprovalOption> = {},
): ApprovalOption {
  return {
    id: "approve_current_action",
    kind: "current_action",
    rung: "once",
    title: "Allow once",
    coverage: "only this exact action",
    expires_when: "after this action",
    reask_when: "the action runs again",
    decision_action: "approve",
    ...overrides,
  };
}

export function approvalPlanFixture(input: PlanFixture = {}): ApprovalPlan {
  const tool = input.tool ?? "command";
  const command = input.command ?? "git status";
  const primaryGate = input.presentation?.gate ?? "explicit_approval_request";
  const options = input.options ?? [approvalOptionFixture()];
  return {
    id: "approval_plan_fixture",
    action_digest: "action_digest_fixture",
    stage: input.stage ?? "pre_spawn",
    subject: {
      kind: "action",
      title: input.title ?? `Approve ${tool}`,
      targets: [{ kind: "action", label: command, details: { tool } }],
      ...input.subject,
    },
    presentation: {
      action: tool === "command" ? "Run command" : `Use ${tool}`,
      tool,
      command,
      impact: input.impact ?? "Run this action.",
      gate: primaryGate,
      cited: [{ gate: primaryGate, key: "action.tool", value: tool, source: "test_fixture" }],
      ...input.presentation,
    },
    reasons: input.reasons ?? [primaryGate],
    options,
    elevated_effects: input.elevatedEffects,
    recommended_option_id:
      input.recommendedOptionId ?? options[0]?.id ?? "approve_current_action",
  };
}

export function toolApprovalFixture(input: PlanFixture = {}): ToolApprovalPayload {
  return {
    plan: approvalPlanFixture(input),
    ...(input.joinedCount ? { joined_count: input.joinedCount } : {}),
    ...(input.repeat ? { repeat: input.repeat } : {}),
  };
}
