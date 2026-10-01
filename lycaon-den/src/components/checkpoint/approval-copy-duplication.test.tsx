import { describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import { toolApprovalFixture } from "../../chat/checkpoint/approval-test-fixtures.ts";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { ApprovalCard } from "./ApprovalCard.tsx";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
vi.mock("../../platform/navigation/open-files-surface.ts", () => ({ openFilesSurface: vi.fn() }));

/** Rendered host copy exposes duplication across card slots. */
function highRiskCheckpoint(
  presentation: Record<string, unknown>,
): PendingCheckpoint {
  return {
    checkpointId: "checkpoint-dup",
    sessionId: "session-1",
    kind: "tool_approval",
    status: "pending",
    issuedAt: "2026-08-08T00:00:00Z",
    tool_approval: toolApprovalFixture({
      command: "docker ps",
      presentation: {
        consequence_band: "high_risk",
        ...presentation,
      } as never,
    }),
  };
}

/** Exact matches handle textContent joining elements without separators. */
function occurrences(text: string, sentence: string): number {
  let count = 0;
  for (let i = text.indexOf(sentence); i !== -1; i = text.indexOf(sentence, i + 1)) {
    count++;
  }
  return count;
}

describe("approval card copy is not duplicated across zones", () => {
  it.each([
    "Changes something on this machine",
    "Changes a remote service",
    "The destination could not be established",
  ])("keeps host-provided detection impact: %s", (impact) => {
    const view = render(() => (
      <ApprovalCard
        projectId="project"
        checkpoint={highRiskCheckpoint({
          consequence_code: "detection",
          impact,
          detection: {
            pack_id: "fixture-pack",
            rule_id: "fixture-rule",
            rule_title: "Confirm the fixture action",
          },
        })}
        resolving={false}
        onToolApproval={() => {}}
        onContentApply={() => {}}
      />
    ));
    const text = view.container.textContent ?? "";
    expect(occurrences(text, impact)).toBe(1);
    fireEvent.click(view.getByRole("button", { name: "Approval details in Files" }));
    const request = vi.mocked(openFilesSurface).mock.lastCall?.[0];
    expect(request?.kind).toBe("chat-content");
    if (request?.kind !== "chat-content" || request.document.content.kind !== "inline") throw new Error("Expected approval details");
    expect(request.document.content.text).toContain("Confirm the fixture action · fixture-pack");
    expect(text).not.toContain("affects an external account, not this machine");
  });

  it("does not repeat the local-socket authority sentence", () => {
    // Host socket-capability presentation.
    const view = render(() => (
      <ApprovalCard
        projectId="project"
        checkpoint={highRiskCheckpoint({
          consequence_code: "local_socket",
          impact: "Connect to 1 local service target(s).",
          who: "a command the agent is running",
          // The boundary description complements the consequence.
          if_wrong:
            "The command still runs inside the filesystem sandbox, and direct outbound network stays blocked unless separately approved. The service behind each socket acts with its own authority outside the sandbox; a container engine can reach any file it mounts.",
        })}
        resolving={false}
        onToolApproval={() => {}}
        onContentApply={() => {}}
      />
    ));
    const sentence = APPROVALS_COPY.card.highRisk.consequence.local_socket;
    expect(occurrences(view.container.textContent ?? "", sentence)).toBe(1);
  });

  it("states one shared package boundary once, not once per member", () => {
    const hosts = ["files.pythonhosted.org", "pypi.org"];
    const view = render(() => (
      <ApprovalCard
        projectId="project"
        checkpoint={{
          checkpointId: "checkpoint-packages",
          sessionId: "session-1",
          kind: "tool_approval",
          status: "pending",
          issuedAt: "2026-08-08T00:00:00Z",
          tool_approval: toolApprovalFixture({
            subject: {
              kind: "package_set",
              title: "Approve command",
              targets: ["pip@26.2.1", "pytest@9.1.1", "requests@2.34.2"].map(
                (label) => ({
                  kind: "package",
                  label,
                  details: {
                    identity_status: "resolved",
                    allowed_hosts: hosts,
                    verified_attestation: true,
                  },
                }),
              ),
            },
          } as never),
        }}
        resolving={false}
        onToolApproval={() => {}}
        onContentApply={() => {}}
      />
    ));
    const text = view.container.textContent ?? "";
    const boundary = APPROVALS_COPY.card.packageIdentity.executionBoundary(hosts);
    expect(occurrences(text, boundary)).toBe(1);
    // The per-package facts still render for every member.
    expect(
      occurrences(text, APPROVALS_COPY.card.packageIdentity.resolved),
    ).toBe(3);
  });

  // The shared boundary remains visible when members open in Files.
  it("states the shared boundary for a set large enough to collapse", () => {
    const hosts = ["pypi.org"];
    const view = render(() => (
      <ApprovalCard
        projectId="project"
        checkpoint={{
          checkpointId: "checkpoint-large",
          sessionId: "session-1",
          kind: "tool_approval",
          status: "pending",
          issuedAt: "2026-08-08T00:00:00Z",
          tool_approval: toolApprovalFixture({
            subject: {
              kind: "package_set",
              title: "Approve command",
              targets: Array.from({ length: 8 }, (_, i) => ({
                kind: "package",
                label: `pkg-${i}@1.0.0`,
                details: { identity_status: "resolved", allowed_hosts: hosts },
              })),
            },
          } as never),
        }}
        resolving={false}
        onToolApproval={() => {}}
        onContentApply={() => {}}
      />
    ));
    expect(view.getByRole("button", {name:/Show all 8 packages in Files/})).toBeTruthy();
    expect(
      occurrences(
        view.container.textContent ?? "",
        APPROVALS_COPY.card.packageIdentity.executionBoundary(hosts),
      ),
    ).toBe(1);
  });

  // Sharing is decided on the hosts, so differing members keep their own line.
  it("keeps per-member boundaries when the registries differ", () => {
    const view = render(() => (
      <ApprovalCard
        projectId="project"
        checkpoint={{
          checkpointId: "checkpoint-mixed",
          sessionId: "session-1",
          kind: "tool_approval",
          status: "pending",
          issuedAt: "2026-08-08T00:00:00Z",
          tool_approval: toolApprovalFixture({
            subject: {
              kind: "package_set",
              title: "Approve command",
              targets: [
                {
                  kind: "package",
                  label: "pip@26.2.1",
                  details: {
                    identity_status: "resolved",
                    allowed_hosts: ["pypi.org"],
                  },
                },
                {
                  kind: "package",
                  label: "left-pad@1.3.0",
                  details: {
                    identity_status: "resolved",
                    allowed_hosts: ["registry.npmjs.org"],
                  },
                },
              ],
            },
          } as never),
        }}
        resolving={false}
        onToolApproval={() => {}}
        onContentApply={() => {}}
      />
    ));
    const text = view.container.textContent ?? "";
    const copy = APPROVALS_COPY.card.packageIdentity;
    expect(view.queryByTestId("approval-targets-shared-fact")).toBeNull();
    expect(occurrences(text, copy.executionBoundary(["pypi.org"]))).toBe(1);
    expect(
      occurrences(text, copy.executionBoundary(["registry.npmjs.org"])),
    ).toBe(1);
  });

  it("keeps the direct-IP impact line reachable", () => {
    // High-risk consequences take precedence over the generic impact.
    const view = render(() => (
      <ApprovalCard
        projectId="project"
        checkpoint={highRiskCheckpoint({
          consequence_code: "direct_ip",
          impact: APPROVALS_COPY.card.highRisk.consequence.direct_ip,
        })}
        resolving={false}
        onToolApproval={() => {}}
        onContentApply={() => {}}
      />
    ));
    const consequence = view.getByTestId("approval-high-risk-consequence");
    expect(consequence.textContent).toBe(
      APPROVALS_COPY.card.highRisk.consequence.direct_ip,
    );
    // The impact slot was taken by the consequence line, so nothing else may be
    // hiding behind it.
    expect(view.queryByTestId("approval-impact")).toBeNull();
  });
});
