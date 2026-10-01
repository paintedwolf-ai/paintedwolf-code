import { stubClient } from "../../test/client-fixture.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import {
  approvalOptionFixture,
  toolApprovalFixture,
} from "../../chat/checkpoint/approval-test-fixtures.ts";
import {
  TranscriptViewportProvider,
  createTranscriptViewportController,
} from "../../chat/stream/transcript-viewport.tsx";
import { ApprovalCard as ApprovalCardView } from "./ApprovalCard.tsx";
import type { ComponentProps } from "solid-js";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
vi.mock("../../platform/navigation/open-files-surface.ts", () => ({openFilesSurface: vi.fn()}));
function ApprovalCard(props: ComponentProps<typeof ApprovalCardView>) {
  return <ApprovalCardView projectId="project" {...props} />;
}
function openedText(view: ReturnType<typeof render>, label = "Approval details in Files"): string {
  fireEvent.click(view.getByRole("button", {name:label}));
  const request = vi.mocked(openFilesSurface).mock.lastCall?.[0];
  if(request?.kind !== "chat-content" || request.document.content.kind !== "inline") throw new Error("Expected recorded approval text");
  return request.document.content.text;
}

const openSourceLocation = vi.hoisted(() => vi.fn());
vi.mock("../../platform/navigation/open-source.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../platform/navigation/open-source.ts")>();
  return { ...actual, openSourceLocation };
});

function checkpoint(): PendingCheckpoint {
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

function directIPCheckpoint(declaredDestinations: string[]): PendingCheckpoint {
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

function socketCheckpoint(): PendingCheckpoint {
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

function contentCheckpoint(): PendingCheckpoint {
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

function secretCheckpoint(): PendingCheckpoint {
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
function unredactableSecretCheckpoint(note: string): PendingCheckpoint {
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

const noopContent = vi.fn();

afterEach(() => vi.clearAllMocks());

describe("ApprovalCard approval plan", () => {
  it("keeps following the transcript while approval details open", () => {
    const controller = createTranscriptViewportController({
      sessionId: () => "session-1",
    });
    const view = render(() => (
      <TranscriptViewportProvider value={controller}>
        <ApprovalCard
          checkpoint={checkpoint()}
          onToolApproval={vi.fn()}
          onContentApply={noopContent}
        />
      </TranscriptViewportProvider>
    ));

    fireEvent.click(
      view.getByRole("button", {name:"Approval details in Files"}),
    );

    expect(controller.following()).toBe(true);
  });

  it("labels direct-IP destinations as declared rather than observed", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={directIPCheckpoint(["db.example.test:5432"])}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    const text = openedText(view);
    expect(text).toContain("db.example.test:5432");
    expect(text).toContain(
      "declared by the agent, not observed by the app",
    );
  });

  it("makes wide direct-IP access explicit when nothing was declared", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={directIPCheckpoint([])}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    expect(openedText(view)).toContain(
      "not narrowed to declared ports",
    );
  });

  it("shows the resolved path behind a requested local-service socket", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={socketCheckpoint()}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    expect(view.getByTestId("approval-subject").textContent).toBe(
      "/tmp/docker.sock",
    );
    expect(openedText(view)).toContain(
      "/private/tmp/docker.sock",
    );
  });

  it("surfaces the detector's credential kind as the approval subject", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={secretCheckpoint()}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    expect(view.getByTestId("approval-subject").textContent).toContain(
      "GitHub Personal Access Token",
    );
  });

  it("renders the host-authored location line under the credential kind", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={secretCheckpoint()}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    expect(view.getByTestId("approval-location-origin").textContent).toBe(
      ".env.local:7",
    );
    expect(view.getByTestId("approval-location-destination").textContent).toBe(
      "Fireworks",
    );
    expect(view.getByTestId("approval-show-in-chat").textContent).toContain(
      "Show in chat",
    );
  });

  it("opens a file origin in Files and keeps Show in chat in Details", async () => {
    openSourceLocation.mockReset();
    openSourceLocation.mockResolvedValue({ status: "opened-in-app" });
    const onShowInChat = vi.fn();
    const view = render(() => (
      <ApprovalCard
        checkpoint={secretCheckpoint()}
        projectId="proj-1"
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
        onShowInChat={onShowInChat}
      />
    ));
    fireEvent.click(view.getByTestId("approval-location-origin"));
    await Promise.resolve();
    expect(openSourceLocation).toHaveBeenCalledWith({
      projectId: "proj-1",
      path: ".env.local",
      line: 7,
      intent: "permanent",
    });
    expect(onShowInChat).not.toHaveBeenCalled();
    fireEvent.click(view.getByTestId("approval-show-in-chat"));
    expect(onShowInChat).toHaveBeenCalledWith("call_read_1");
  });

  it("uses a field origin click as Show in chat", () => {
    const onShowInChat = vi.fn();
    const checkpoint = secretCheckpoint();
    checkpoint.tool_approval = toolApprovalFixture({
      tool: "web_search",
      stage: "pre_send",
      subject: {
        kind: "secret",
        title: "Credential detected before sending this web search",
        targets: [{
          kind: "secret",
          label: "GitHub Personal Access Token",
          details: {
            generic_shape: "abc-a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3 (40 characters)",
          },
        }],
      },
      presentation: {
        action: "Send web search",
        gate: "secret_outbound",
        impact: "This action would expose a credential outside protected secret storage.",
        location: {
          origin: "in query",
          destination: "configured web search providers",
          origin_kind: "field",
          destination_kind: "service",
          reveal_tool_call_id: "call_search_1",
        },
      },
    });
    const view = render(() => (
      <ApprovalCard
        checkpoint={checkpoint}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
        onShowInChat={onShowInChat}
      />
    ));
    fireEvent.click(view.getByTestId("approval-location-origin"));
    expect(onShowInChat).toHaveBeenCalledWith("call_search_1");
    expect(view.queryByTestId("approval-show-in-chat")).toBeNull();
  });

  it("always shows the host-generated generic secret shape", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={secretCheckpoint()}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    const shape = view.getByTestId("approval-secret-shape");
    expect(shape.textContent).toContain("Looks like");
    expect(shape.textContent).toContain(
      "abc-a1b2c3a1b2c3",
    );
    expect(shape.textContent).not.toContain("ghp_");
  });

  it("states why a secret card's redacted send is unavailable", () => {
    const note = "Redaction is not offered here: replacing the value would run a"
      + " command neither you nor the model wrote.";
    const view = render(() => (
      <ApprovalCard
        checkpoint={unredactableSecretCheckpoint(note)}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    expect(view.getByTestId("approval-option-note").textContent).toContain(note);
    const primary = view.getByTestId(
      "approval-approve-primary",
    ) as HTMLButtonElement;
    expect(primary.textContent).toContain("Send redacted");
    expect(primary.disabled).toBe(true);
  });

  it("leaves the note off a card that does offer redaction", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={secretCheckpoint()}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    expect(view.queryByTestId("approval-option-note")).toBeNull();
  });

  it("shows args for a singleton action_set, not only the tool id", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={{
          ...checkpoint(),
          tool_approval: toolApprovalFixture({
            tool: "command",
            title: "Approve 1 planned actions",
            impact: "Approve an exact set of 1 planned actions",
            subject: {
              kind: "action_set",
              title: "Approve 1 planned actions",
              targets: [{
                kind: "action",
                label: "command",
                details: {
                  tool: "command",
                  args: { argv: ["go", "test", "./internal/hitl/..."] },
                },
              }],
            },
            presentation: {
              action: "Approve 1 planned actions",
              tool: "command",
              command: "",
              gate: "explicit_approval_request",
              impact: "Approve an exact set of 1 planned actions",
            },
          }),
        }}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    const target = view.getByTestId("approval-target-0");
    expect(target.textContent).toContain("command");
    expect(target.textContent).toContain("go");
    expect(target.textContent).toContain("./internal/hitl/...");
    expect(view.queryByTestId("approval-subject")).toBeNull();
  });

  it("renders only host-reviewed plan fields", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={checkpoint()}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    expect(view.getByTestId("approval-subject").textContent).toContain(
      "git push origin main",
    );
    expect(view.queryByTestId("approval-causing-command")).toBeNull();
    expect(view.getByTestId("approval-impact").textContent).toContain(
      "Push commits",
    );
    expect(view.getByTestId("approval-approve-primary").textContent).toContain(
      "Allow for this chat",
    );
  });

  it("shows the command that needed a write root under the path subject", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={{
          ...checkpoint(),
          tool_approval: toolApprovalFixture({
            tool: "command",
            command: "go mod tidy",
            title: "Allow write access",
            impact: "Allow future commands in this chat to write within the listed root.",
            stage: "pre_spawn",
            subject: {
              kind: "write_root_set",
              title: "Allow write access",
              targets: [{
                kind: "write_root",
                label: "/Users/example/go",
                details: {
                  blocked_path: "/Users/example/go/pkg/sumdb/sum.golang.org/latest",
                },
              }],
            },
            presentation: {
              action: "Sandbox write access",
              tool: "command",
              command: "go mod tidy",
              gate: "outside_roots_write",
              impact: "Allow future commands in this chat to write within the listed root.",
            },
          }),
        }}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    expect(view.getByTestId("approval-subject").textContent).toContain(
      "/Users/example/go",
    );
    const cause = view.getByTestId("approval-causing-command");
    expect(cause.textContent).toContain("From");
    expect(cause.textContent).toContain("go mod tidy");
    expect(view.getByTestId("approval-tool").textContent).toContain("command");
  });

  it("submits the exact opaque option id", () => {
    const onToolApproval = vi.fn();
    const view = render(() => (
      <ApprovalCard
        checkpoint={checkpoint()}
        onToolApproval={onToolApproval}
        onContentApply={noopContent}
      />
    ));
    fireEvent.click(view.getByTestId("approval-approve-primary"));
    expect(onToolApproval).toHaveBeenCalledWith("approve", {
      optionId: "allow_for_chat",
    });
  });

  it("keeps rejection separate from affirmative options", () => {
    const onToolApproval = vi.fn();
    const view = render(() => (
      <ApprovalCard
        checkpoint={checkpoint()}
        onToolApproval={onToolApproval}
        onContentApply={noopContent}
      />
    ));
    fireEvent.click(view.getByTestId("approval-no"));
    expect(onToolApproval).toHaveBeenCalledWith("reject", {
      withGuidance: false,
    });
  });

  it("faces the host recommended_option_id without inventing a fallback", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={checkpoint()}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    expect(view.getByTestId("approval-approve-primary").textContent).toContain(
      "Allow for this chat",
    );
  });

  it("renders host ladder order with non-focusable group headings", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={{
          ...checkpoint(),
          tool_approval: toolApprovalFixture({
            recommendedOptionId: "allow_for_chat",
            options: [
              approvalOptionFixture({ id: "approve_current_action", rung: "once" }),
              approvalOptionFixture({
                id: "allow_1d",
                kind: "lease",
                rung: "day",
                scope: "project",
                title: "Allow for 1 day",
                expires_when: "in 1 day or when revoked",
              }),
              approvalOptionFixture({
                id: "allow_for_chat",
                kind: "lease",
                rung: "chat",
                scope: "chat",
                title: "Allow for this chat",
                expires_when: "when this chat is deleted",
              }),
              approvalOptionFixture({
                id: "also_chat",
                kind: "lease",
                rung: "chat",
                scope: "chat",
                group: "Also allow",
                title: "Allow for this chat",
                coverage: "api.example.com",
                expires_when: "when this chat is deleted",
              }),
            ],
          }),
        }}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    fireEvent.click(view.getByTestId("approval-grant-face"));
    const menu = screen.getByTestId("approval-grant-menu");
    const titles = [...menu.querySelectorAll(".den-approval-grant-item-title")].map(
      (el) => el.textContent,
    );
    // Alternate grants retain host order.
    expect(titles).toEqual([
      "Allow once",
      "Allow for 1 day",
      "Allow for this chat",
    ]);
    const heading = screen.getByTestId("approval-grant-group");
    expect(heading.textContent).toBe("Also allow");
    expect(heading.getAttribute("role")).toBe("presentation");
    expect(menu.querySelectorAll("[role=menuitem]").length).toBe(3);
    expect(menu.querySelectorAll("[data-testid=approval-grant-sep]").length).toBe(1);
    expect(
      [...menu.querySelectorAll("[role=menuitem]")].every(
        (el) => !el.classList.contains("den-approval-grant-group"),
      ),
    ).toBe(true);
    // Every grant row exposes its coverage.
    const metas = [...menu.querySelectorAll("[data-testid=approval-grant-item-meta]")].map(
      (el) => el.textContent,
    );
    // The primary grant is omitted from the menu.
    expect(metas).toEqual([
      "only this exact action",
      "only this exact action",
      "api.example.com",
    ]);
    // Digits follow the rung, and the grouped second subject carries none.
    expect(
      [...menu.querySelectorAll("[role=menuitem]")].map((el) => el.getAttribute("data-slot")),
    ).toEqual(["1", "2", null]);
  });

  it("renders Allow and stop asking rungs while honoring the host face", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={{
          ...checkpoint(),
          tool_approval: toolApprovalFixture({
            recommendedOptionId: "approve_current_action",
            options: [
              approvalOptionFixture({
                id: "approve_current_action",
                rung: "once",
                title: "Allow once",
              }),
              approvalOptionFixture({
                id: "quiet_day",
                kind: "quiet",
                rung: "day",
                group: "Allow and stop asking",
                title: "For 1 day",
                coverage: "aws-cli / s3-remove-bucket",
                expires_when: "in 1 day",
              }),
              approvalOptionFixture({
                id: "quiet_chat",
                kind: "quiet",
                rung: "chat",
                group: "Allow and stop asking",
                title: "For this chat",
                coverage: "aws-cli / s3-remove-bucket",
                expires_when: "when this chat is deleted or you revoke it",
              }),
            ],
          }),
        }}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    expect(view.getByTestId("approval-approve-primary").textContent).toContain(
      "Allow once",
    );
    fireEvent.click(view.getByTestId("approval-grant-face"));
    const menu = screen.getByTestId("approval-grant-menu");
    const titles = [...menu.querySelectorAll(".den-approval-grant-item-title")].map(
      (el) => el.textContent,
    );
    expect(titles).toEqual(["For 1 day", "For this chat"]);
    expect(screen.getByTestId("approval-grant-group").textContent).toBe(
      "Allow and stop asking",
    );
    const metas = [...menu.querySelectorAll("[data-testid=approval-grant-item-meta]")].map(
      (el) => el.textContent,
    );
    expect(metas).toEqual([
      "aws-cli / s3-remove-bucket",
      "aws-cli / s3-remove-bucket",
    ]);
  });
});

describe("ApprovalCard content plan", () => {
  it("approves the complete host proposal when every hunk remains selected", () => {
    const onContentApply = vi.fn();
    const view = render(() => (
      <ApprovalCard
        checkpoint={contentCheckpoint()}
        onToolApproval={vi.fn()}
        onContentApply={onContentApply}
      />
    ));
    fireEvent.click(view.getByTestId("content-apply-approve"));
    expect(onContentApply).toHaveBeenCalledWith({ decision: "approve" });
  });

  it("submits only selected host hunk ids for partial approval", () => {
    const onContentApply = vi.fn();
    const view = render(() => (
      <ApprovalCard
        checkpoint={contentCheckpoint()}
        onToolApproval={vi.fn()}
        onContentApply={onContentApply}
      />
    ));
    const checkboxes = view.getAllByRole("checkbox") as HTMLInputElement[];
    fireEvent.click(checkboxes[0]!);
    fireEvent.click(view.getByTestId("content-apply-approve"));
    expect(onContentApply).toHaveBeenCalledWith({
      decision: "approve_partial",
      approvedHunks: ["host-hunk-2"],
    });
    expect(view.queryByTestId("content-apply-edit")).toBeNull();
  });

  it("does not allow an empty hunk selection to masquerade as full approval", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={contentCheckpoint()}
        onToolApproval={vi.fn()}
        onContentApply={vi.fn()}
      />
    ));
    for (const checkbox of view.getAllByRole("checkbox")) {
      fireEvent.click(checkbox);
    }
    expect(
      (view.getByTestId("content-apply-approve") as HTMLButtonElement).disabled,
    ).toBe(true);
  });
});

describe("evidence on the card", () => {
  function evidenceCheckpoint(): PendingCheckpoint {
    return {
      checkpointId: "checkpoint-evidence",
      sessionId: "session-1",
      kind: "tool_approval",
      status: "pending",
      tool_approval: toolApprovalFixture({
        tool: "write",
        command: "",
        reasons: ["sensitive_location", "outside_roots_write"],
        presentation: {
          gate: "sensitive_location",
          cited: [
            { gate: "sensitive_location", key: "file.path", value: "/Users/x/.zshrc", source: "confine" },
            {
              gate: "sensitive_location",
              key: "location.catalog",
              value: "Shell startup file",
              source: "sensitive_locations",
            },
            {
              gate: "outside_roots_write",
              key: "file.outside_roots",
              value: "true",
              source: "boundary",
            },
          ],
        },
      }),
    } as PendingCheckpoint;
  }

  it("names the gate and lists what was observed", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={evidenceCheckpoint()}
        resolving={false}
        onToolApproval={() => {}}
        onContentApply={noopContent}
      />
    ));
    const text = openedText(view);
    expect(text).toContain("This is a sensitive location outside your project");
    expect(text).toContain("file.path: /Users/x/.zshrc (confine)");
    expect(text).toContain("location.catalog: Shell startup file (sensitive_locations)");
    expect(text).toContain("Observed · This writes outside your attached folders");
  });

  it("shows extension policy provenance from the host", () => {
    const cp = evidenceCheckpoint();
    if (cp.tool_approval) {
      cp.tool_approval.plan.presentation.approval_rules = [{
        category: "command",
        pattern: "git push*",
        effect: "ask",
        unit_id: "approvals/rules/release",
        pack_id: "acme/policy",
        scope: "project",
      }];
    }
    const view = render(() => (
      <ApprovalCard
        checkpoint={cp}
        resolving={false}
        onToolApproval={() => {}}
        onContentApply={noopContent}
      />
    ));
    const text = openedText(view);
    expect(text).toContain("Policy");
    expect(text).toContain("Ask command:git push* · Repository · acme/policy");
  });

  it("carries no external-content provenance", () => {
    const { queryByTestId } = render(() => (
      <ApprovalCard
        checkpoint={evidenceCheckpoint()}
        resolving={false}
        onToolApproval={() => {}}
        onContentApply={noopContent}
      />
    ));
    expect(queryByTestId("approval-external-content-chip")).toBeNull();
  });
});

describe("ApprovalCard repeat block", () => {
  it("hides repeat when count is 1", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={{
          checkpointId: "cp-repeat-1",
          sessionId: "session-1",
          kind: "tool_approval",
          status: "pending",
          issuedAt: "2026-08-06T00:00:00Z",
          tool_approval: toolApprovalFixture({
            repeat: {
              reason_key: "authority_misuse:aws-cli/s3-remove-bucket",
              count: 1,
              subjects: [],
            },
          }),
        }}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    expect(view.queryByTestId("approval-repeat")).toBeNull();
  });

  it("shows repeat summary, scope note, and prior subjects when count is 4", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={{
          checkpointId: "cp-repeat-4",
          sessionId: "session-1",
          kind: "tool_approval",
          status: "pending",
          issuedAt: "2026-08-06T00:00:00Z",
          tool_approval: toolApprovalFixture({
            command: "aws s3 rb s3://d",
            repeat: {
              reason_key: "authority_misuse:aws-cli/s3-remove-bucket",
              count: 4,
              subjects: [
                "aws s3 rb s3://a",
                "aws s3 rb s3://b",
                "aws s3 rb s3://c",
              ],
            },
          }),
        }}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    expect(view.getByTestId("approval-repeat").textContent).toContain(
      "Asked 4 times for this reason since your last message",
    );
    const text = openedText(view, "Repeated requests in Files");
    expect(text).toContain("Each was decided separately.");
    expect(text).toContain("aws s3 rb s3://a");
    expect(text).toContain("aws s3 rb s3://c");
    expect(view.queryByTestId("approval-repeat-subjects")).toBeNull();
  });

  it("renders joined waiters and repeat together", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={{
          checkpointId: "cp-both",
          sessionId: "session-1",
          kind: "tool_approval",
          status: "pending",
          issuedAt: "2026-08-06T00:00:00Z",
          tool_approval: toolApprovalFixture({
            joinedCount: 3,
            repeat: {
              reason_key: "authority_misuse:aws-cli/s3-remove-bucket",
              count: 4,
              subjects: ["aws s3 rb s3://a"],
            },
          }),
        }}
        onToolApproval={vi.fn()}
        onContentApply={noopContent}
      />
    ));
    expect(view.getByTestId("approval-joined-waiters").textContent).toContain(
      "3 identical calls waiting",
    );
    expect(view.getByTestId("approval-repeat").textContent).toContain(
      "Asked 4 times for this reason",
    );
  });
});



describe("Service secret permissions", () => {
  it("shows every recipient and keeps the chat choice on the primary button", () => {
    const item = checkpoint();
    const approval = item.tool_approval!;
    approval.plan.subject = {
      kind: "action_set", title: "Use the local service",
      targets: [
        { kind: "loopback_connect", label: "Local connections on port 8080" },
        { kind: "secret", label: "Service password" },
      ],
    };
    approval.plan.presentation.location = {
      origin: "in arguments", origin_kind: "field",
      destination: "Processes in this chat", destination_kind: "process",
      secret_names: ["Service password"],
      recipients: [
        { label: "Processes in this chat", surface: "command", kind: "process" },
        { label: "http://127.0.0.1:8080", surface: "http_request", kind: "service" },
      ],
    };
    const onToolApproval = vi.fn();
    const view = render(() => <ApprovalCard checkpoint={item} onToolApproval={onToolApproval} onContentApply={noopContent} />);
    expect(view.getByTestId("approval-secret-names").textContent).toContain("Service password");
    expect(view.getAllByText("Service password")).toHaveLength(1);
    expect(view.getAllByTestId("approval-location-destination").map((row) => row.textContent)).toEqual([
      "Processes in this chat", "http://127.0.0.1:8080",
    ]);
    expect(view.getByTestId("approval-approve-primary").textContent).toContain("Allow for this chat");
    fireEvent.click(view.getByTestId("approval-approve-primary"));
    expect(onToolApproval).toHaveBeenCalled();
  });
});

it("writes a reviewed project ignore without resolving the held approval", async () => {
  const checkpoint = secretCheckpoint();
  checkpoint.tool_approval!.plan.presentation.ignore_candidate_id = "candidate";
  const add = vi.fn(async () => ({ rules: [], invalid: [] }));
  const approve = vi.fn();
  const client = stubClient({
    getProject: vi.fn(async () => ({ roots: [{ id: "root", label: "App", path: "/app" }] })),
    getSecretIgnoreCandidate: vi.fn(async () => ({ value: "public fixture" })),
    createProjectSecretIgnore: add,
  });
  render(() => <ApprovalCard client={client} projectId="project" checkpoint={checkpoint} onToolApproval={approve} onContentApply={noopContent} />);
  fireEvent.click(screen.getByRole("button", { name: "Ignore this value in this project…" }));
  const reason = await screen.findByLabelText("Reason");
  fireEvent.input(reason, { target: { value: "Published example" } });
  fireEvent.click(screen.getByRole("button", { name: "Save project ignore" }));
  await waitFor(() => expect(add).toHaveBeenCalledOnce());
  expect(approve).not.toHaveBeenCalled();
  await screen.findByText("Project ignore saved. The held request still needs your decision.");
});

describe("ApprovalCard shortcuts", () => {
  function mountWithoutGlobalKeyListeners(checkpoint: PendingCheckpoint) {
    const onToolApproval = vi.fn();
    const onContentApply = vi.fn();
    const documentListen = vi.spyOn(document, "addEventListener");
    const windowListen = vi.spyOn(window, "addEventListener");
    const view = render(() => (
      <ApprovalCard
        checkpoint={checkpoint}
        onToolApproval={onToolApproval}
        onContentApply={onContentApply}
      />
    ));
    const keyListeners = [...documentListen.mock.calls, ...windowListen.mock.calls]
      .filter(([type]) => type === "keydown" || type === "keyup");
    documentListen.mockRestore();
    windowListen.mockRestore();
    expect(keyListeners).toEqual([]);
    return { view, onToolApproval, onContentApply };
  }

  it("answers Enter on the tool card and ignores keys pressed outside it", () => {
    const { view, onToolApproval } = mountWithoutGlobalKeyListeners(checkpoint());
    const outside = document.createElement("div");
    outside.tabIndex = 0;
    document.body.append(outside);
    outside.focus();
    fireEvent.keyDown(outside, { key: "Enter" });
    expect(onToolApproval).not.toHaveBeenCalled();
    outside.remove();

    const card = view.getByTestId("tool-approval-card");
    card.focus();
    fireEvent.keyDown(card, { key: "Enter" });
    expect(onToolApproval).toHaveBeenCalledWith("approve", { optionId: "allow_for_chat" });
  });

  it("answers Enter on the content card and ignores keys pressed outside it", () => {
    const { view, onContentApply } = mountWithoutGlobalKeyListeners(contentCheckpoint());
    fireEvent.keyDown(document.body, { key: "Enter" });
    expect(onContentApply).not.toHaveBeenCalled();

    const card = view.getByTestId("content-apply-card");
    card.focus();
    fireEvent.keyDown(card, { key: "Enter" });
    expect(onContentApply).toHaveBeenCalledWith({ decision: "approve" });
  });
});
