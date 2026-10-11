
import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";

import { approvalOptionFixture, toolApprovalFixture } from "../../chat/checkpoint/approval-test-fixtures.ts";
import { TranscriptViewportProvider, createTranscriptViewportController } from "../../chat/stream/transcript-viewport.tsx";
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

import { checkpoint, directIPCheckpoint, socketCheckpoint, secretCheckpoint, unredactableSecretCheckpoint } from "./approval-card-test-fixtures.ts";
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
