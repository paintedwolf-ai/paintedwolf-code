import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import { approvalOptionFixture, toolApprovalFixture } from "../../chat/checkpoint/approval-test-fixtures.ts";

export function checkpoint(): PendingCheckpoint {
  return {
    checkpointId: "checkpoint-1",
    sessionId: "session-1",
    kind: "tool_approval",
    status: "pending",
    issuedAt: "2026-08-06T00:00:00Z",
    tool_approval: toolApprovalFixture({
      command: "git push origin main",
      impact: "Push commits to the shared repository.",
      recommendedOptionId: "allow_for_chat",
      options: [
        approvalOptionFixture({ id: "approve_current_action", rung: "once" }),
        approvalOptionFixture({
          id: "allow_for_chat",
          kind: "lease",
          rung: "chat",
          scope: "chat",
          title: "Allow for this chat",
          coverage: "this exact command",
          expires_when: "when this chat is deleted",
          reask_when: "the command changes",
        }),
      ],
    }),
  };
}

export function directIPCheckpoint(declaredDestinations: string[]): PendingCheckpoint {
  return {
    checkpointId: "direct-ip-checkpoint-1",
    sessionId: "session-1",
    kind: "tool_approval",
    status: "pending",
    issuedAt: "2026-08-06T00:00:00Z",
    tool_approval: toolApprovalFixture({
      command: "psql -h db.example.test",
      subject: {
        kind: "direct_ip",
        title: "Allow direct network access",
        targets: [{
          kind: "direct_ip",
          label: "psql -h db.example.test",
          details: {
            visibility: "unobserved",
            declared_destinations: declaredDestinations,
          },
        }],
      },
      presentation: {
        action: "Use direct network access",
        consequence_band: "high_risk",
        consequence_code: "direct_ip",
      },
    }),
  };
}

export function socketCheckpoint(): PendingCheckpoint {
  return {
    checkpointId: "socket-checkpoint-1",
    sessionId: "session-1",
    kind: "tool_approval",
    status: "pending",
    issuedAt: "2026-08-06T00:00:00Z",
    tool_approval: toolApprovalFixture({
      command: "docker ps",
      subject: {
        kind: "socket_set",
        title: "Allow local services",
        targets: [{
          kind: "socket",
          label: "/tmp/docker.sock",
          details: {
            approved_path: "/tmp/docker.sock",
            resolved_path: "/private/tmp/docker.sock",
          },
        }],
      },
      presentation: {
        action: "Use command",
        consequence_band: "high_risk",
        consequence_code: "local_socket",
      },
    }),
  };
}

export function contentCheckpoint(): PendingCheckpoint {
  return {
    checkpointId: "content-checkpoint-1",
    sessionId: "session-1",
    kind: "content_apply",
    status: "pending",
    issuedAt: "2026-08-06T00:00:00Z",
    content_apply: {
      tool: "edit",
      path: "notes.txt",
      before: "one\nkeep\nthree\n",
      after: "ONE\nkeep\nTHREE\n",
      hunks: [
        { id: "host-hunk-1", path: "notes.txt", before: "one", after: "ONE" },
        { id: "host-hunk-2", path: "notes.txt", before: "three", after: "THREE" },
      ],
    },
  };
}

export function secretCheckpoint(): PendingCheckpoint {
  return {
    checkpointId: "secret-checkpoint-1",
    sessionId: "session-1",
    kind: "tool_approval",
    status: "pending",
    issuedAt: "2026-08-06T00:00:00Z",
    tool_approval: toolApprovalFixture({
      tool: "model_request",
      stage: "pre_send",
      subject: {
        kind: "secret",
        title: "Credential detected before sending this model request",
        targets: [{
          kind: "secret",
          label: "GitHub Personal Access Token",
          details: {
            generic_shape: "abc-a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3 (40 characters)",
          },
        }],
      },
      presentation: {
        action: "Send model request",
        gate: "secret_outbound",
        impact: "This action would expose a credential outside protected secret storage.",
        location: {
          origin: ".env.local:7",
          destination: "Fireworks",
          origin_kind: "file",
          destination_kind: "service",
          path: ".env.local",
          line: 7,
          reveal_tool_call_id: "call_read_1",
        },
      },
      recommendedOptionId: "send_redacted",
      options: [approvalOptionFixture({
        id: "send_redacted",
        kind: "redacted",
        rung: "redacted",
        title: "Send redacted",
        decision_action: "redact",
      })],
    }),
  };
}

// This execution seam cannot redact the credential.
export function unredactableSecretCheckpoint(note: string): PendingCheckpoint {
  return {
    checkpointId: "secret-checkpoint-2",
    sessionId: "session-1",
    kind: "tool_approval",
    status: "pending",
    issuedAt: "2026-08-06T00:00:00Z",
    tool_approval: toolApprovalFixture({
      tool: "command",
      stage: "pre_send",
      subject: {
        kind: "secret",
        title: "Credential detected before sending this command",
        targets: [{
          kind: "secret",
          label: "GitHub Personal Access Token",
          details: {
            generic_shape: "abc-a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3 (40 characters)",
          },
        }],
      },
      presentation: {
        action: "Send command",
        gate: "secret_outbound",
        impact: "This action would expose a credential outside protected secret storage.",
        option_note: note,
      },
      recommendedOptionId: "send_redacted",
      options: [
        approvalOptionFixture({
          id: "send_redacted",
          kind: "redacted",
          rung: "redacted",
          title: "Send redacted",
          decision_action: "redact",
          disabled: true,
        }),
        approvalOptionFixture({
          id: "send_unchanged",
          rung: "unchanged",
          title: "Send unchanged",
        }),
      ],
    }),
  };
}

