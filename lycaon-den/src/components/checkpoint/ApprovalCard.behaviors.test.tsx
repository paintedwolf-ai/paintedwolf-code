import { stubClient } from "../../test/client-fixture.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import { approvalOptionFixture, toolApprovalFixture } from "../../chat/checkpoint/approval-test-fixtures.ts";

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

import { checkpoint, contentCheckpoint, secretCheckpoint } from "./approval-card-test-fixtures.ts";
const noopContent = vi.fn();

afterEach(() => vi.clearAllMocks());

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

describe("held release", () => {
  function heldCheckpoint(): PendingCheckpoint {
    const value = checkpoint();
    value.tool_approval!.plan.held_release = {
      chat_session_id: "session-1",
      secrets: [{ reference: "{{paintedwolf-secret:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa}}", name: "Deploy key", version: 1 }],
      recipients: [{ label: "Local file: .env", surface: "file", kind: "file" }],
    };
    return value;
  }

  // Tests run outside the desktop shell, which alone can confirm the person.
  it("names the values and who can read them, and leaves approval to the desktop app", () => {
    const onToolApproval = vi.fn();
    const view = render(() => (
      <ApprovalCard checkpoint={heldCheckpoint()} onToolApproval={onToolApproval} onContentApply={noopContent} />
    ));
    const notice = view.getByTestId("approval-held-release");
    expect(notice.textContent).toContain("Deploy key");
    expect(notice.textContent).toContain("Anything that can read this project's files can read it.");
    expect(view.getByTestId("approval-held-confirm").textContent).toContain("installed desktop app");
    const primary = view.getByTestId("approval-approve-primary") as HTMLButtonElement;
    expect(primary.disabled).toBe(true);
    fireEvent.click(primary);
    expect(onToolApproval).not.toHaveBeenCalled();
  });
});

describe("directory scope", () => {
  it("defaults to the containing folder and dispatches the selected host option", () => {
    const onToolApproval = vi.fn();
    const narrow = "/workspace/src/pkg";
    const broad = "/workspace";
    const cp = checkpoint();
    cp.tool_approval = toolApprovalFixture({
      tool: "read",
      directoryScopes: [narrow, broad],
      options: [narrow, broad].map((path, index) => approvalOptionFixture({
        id: `chat-${index}`, kind: "lease", rung: "chat", scope: "chat",
        title: "Allow for this chat", directory_scope: path,
        coverage: `reads of ${path}`,
      })),
      recommendedOptionId: "chat-0",
    });
    const view = render(() => <ApprovalCard checkpoint={cp} resolving={false} onToolApproval={onToolApproval} onContentApply={vi.fn()} />);
    const selector = view.getByRole("button", {name: "Directory scope"});
    expect(selector.textContent).toContain(narrow);
    fireEvent.keyDown(selector, {key:"Enter"});
    expect(onToolApproval).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("option", {name:broad}));
    expect(selector.textContent).toContain(broad);
    fireEvent.keyDown(view.getByTestId("tool-approval-card"), {key:"3"});
    expect(onToolApproval).toHaveBeenCalledWith("approve", {optionId:"chat-1"});
  });
});
