import { stubClient } from "../../test/client-fixture.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import { CheckpointDecisionChicklet as DecisionView } from "./CheckpointDecisionChicklet.tsx";
import type { ComponentProps } from "solid-js";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
vi.mock("../../platform/navigation/open-files-surface.ts", () => ({openFilesSurface: vi.fn()}));
function CheckpointDecisionChicklet(props: ComponentProps<typeof DecisionView>) {
  return <DecisionView projectId="project" sessionId="session" {...props} />;
}
function decisionText(): string {
  const request = vi.mocked(openFilesSurface).mock.lastCall?.[0];
  if(request?.kind !== "chat-content" || request.document.content.kind !== "inline") throw new Error("Expected recorded approval text");
  expect(request.document.kind).toBe("approval");
  return request.document.content.text;
}
import type { CheckpointDecisionMeta } from "../../api/types.ts";
import { invalidateApprovalGrantsCache } from "../../settings/security/approval-grants-cache.ts";
import { approvalReviewTarget } from "../../chat/checkpoint/approval-review.ts";

describe("CheckpointDecisionChicklet", () => {
  beforeEach(() => {
    invalidateApprovalGrantsCache();
    vi.clearAllMocks();
  });

  it("shows the persisted secret location on the decision", () => {
    const meta: CheckpointDecisionMeta = {
      checkpoint_id: "chk-secret",
      kind: "tool_approval",
      status: "approved",
      tool: "model request",
      subject: "GitHub Personal Access Token",
      location: ".env.local:7 → Fireworks",
    };
    const { getByTestId } = render(() => (
      <CheckpointDecisionChicklet meta={meta} entryKey="checkpoint:secret" />
    ));
    expect(getByTestId("checkpoint-decision-location").textContent).toBe(
      ".env.local:7 → Fireworks",
    );
  });

  it("records an allowed tool approval choice with subject details", () => {
    const meta: CheckpointDecisionMeta = {
      checkpoint_id: "chk-1",
      kind: "tool_approval",
      status: "approved",
      tool: "command",
      subject: "git push --force",
    };
    const { getByTestId } = render(() => (
      <CheckpointDecisionChicklet meta={meta} entryKey="checkpoint:a" />
    ));
    const el = getByTestId("checkpoint-decision-chicklet");
    expect(el.tagName).toBe("DETAILS");
    expect(el.getAttribute("data-status")).toBe("approved");
    expect(el.getAttribute("data-kind")).toBe("tool_approval");
    expect(el.textContent).toContain("Allowed");
    expect(getByTestId("checkpoint-decision-tool").textContent).toBe("command");
    expect(getByTestId("checkpoint-decision-subject").textContent).toBe("git push --force");
  });

  it("shows a scoped option and revokes every installed grant", async () => {
    const grantId = "host-grant-git-status";
    const secondGrantId = "host-grant-direct-network";
    const meta: CheckpointDecisionMeta = {
      checkpoint_id: "chk-grant",
      kind: "tool_approval",
      status: "approved",
      tool: "command",
      subject: "git status -s",
      grant_scope: "chat",
      grant_title: "Allow for this chat",
      grant_ids: [grantId, secondGrantId],
    };
    const listApprovalGrants = vi
      .fn()
      .mockResolvedValueOnce({
        grants: [
          {
            id: grantId,
            scope: "chat",
            category: "action_set",
            pattern: "exact-action-digest",
            title: "Allow for this chat",
            coverage: "the exact action set",
            granted_at: "2026-01-01T00:00:00Z",
            expires_when: "when this chat is deleted",
            reask_when: "the action changes",
          },
          {
            id: secondGrantId,
            scope: "chat",
            category: "direct_ip",
            pattern: "direct-action-digest",
            title: "Allow direct network for this chat",
            coverage: "the same exact action",
            granted_at: "2026-01-01T00:00:00Z",
            expires_when: "when this chat is deleted",
            reask_when: "the action changes",
          },
        ],
      })
      .mockResolvedValue({ grants: [] });
    const revokeApprovalGrants = vi
      .fn()
      .mockImplementation(async ({ ids }: { ids: string[] }) => ({
        results: ids.map((id) => ({ id, revoked: true })),
      }));
    const updateApprovalsSettings = vi.fn();
    const client = stubClient({
      listApprovalGrants,
      revokeApprovalGrants,
      updateApprovalsSettings,
    });

    const { getByTestId, queryByTestId } = render(() => (
      <CheckpointDecisionChicklet
        meta={meta}
        entryKey="checkpoint:grant"
        client={client}
      />
    ));

    expect(getByTestId("checkpoint-decision-grant").textContent).toBe(
      "Allow for this chat · chat",
    );
    await vi.waitFor(() => {
      expect(listApprovalGrants).toHaveBeenCalledTimes(1);
      expect(getByTestId("checkpoint-decision-revoke")).toBeTruthy();
    });
    fireEvent.click(getByTestId("checkpoint-decision-revoke"));
    await vi.waitFor(() => {
      expect(revokeApprovalGrants).toHaveBeenCalledWith({
        ids: [grantId, secondGrantId],
      });
      expect(updateApprovalsSettings).not.toHaveBeenCalled();
      expect(queryByTestId("checkpoint-decision-revoke")).toBeNull();
    });
    // Revocation preserves the recorded grant label.
    expect(getByTestId("checkpoint-decision-grant").textContent).toBe(
      "Allow for this chat · chat",
    );
  });

  it("hides Revoke when every grant_ids member is absent from the shared cache", async () => {
    const meta: CheckpointDecisionMeta = {
      checkpoint_id: "chk-gone",
      kind: "tool_approval",
      status: "approved",
      tool: "command",
      subject: "git status",
      grant_scope: "chat",
      grant_title: "Allow for this chat",
      grant_ids: ["already-revoked"],
    };
    const listApprovalGrants = vi.fn().mockResolvedValue({ grants: [] });
    const client = stubClient({ listApprovalGrants });

    const { getByTestId, queryByTestId } = render(() => (
      <CheckpointDecisionChicklet meta={meta} client={client} />
    ));

    expect(getByTestId("checkpoint-decision-grant")).toBeTruthy();
    await vi.waitFor(() => {
      expect(listApprovalGrants).toHaveBeenCalled();
    });
    expect(queryByTestId("checkpoint-decision-revoke")).toBeNull();
  });

  it("opens the full recorded subject in Files", () => {
    const meta: CheckpointDecisionMeta = {
      checkpoint_id: "chk-2",
      kind: "tool_approval",
      status: "rejected",
      tool: "command",
      subject: "rm -rf /very/long/path/that/would/truncate",
    };
    const { getByTestId } = render(() => (
      <CheckpointDecisionChicklet meta={meta} entryKey="checkpoint:b" />
    ));
    const el = getByTestId("checkpoint-decision-chicklet") as HTMLDetailsElement;
    expect(el.open).toBe(false);
    fireEvent.click(el.querySelector("summary")!);
    expect(el.open).toBe(true);
    fireEvent.click(getByTestId("checkpoint-decision-body").querySelector("button")!);
    expect(decisionText()).toContain("rm -rf /very/long/path/that/would/truncate");
    expect(getByTestId("checkpoint-decision-body").querySelector("pre")).toBeNull();
  });

  it("keeps denial direction visible in the durable decision", () => {
    const meta: CheckpointDecisionMeta = {
      checkpoint_id: "chk-guidance",
      kind: "tool_approval",
      status: "rejected",
      tool: "command",
      subject: "rm generated.txt",
      guidance: "Keep the file and explain why it is obsolete.",
    };
    const { getByTestId } = render(() => (
      <CheckpointDecisionChicklet
        meta={meta}
        entryKey="checkpoint:guidance"
      />
    ));
    fireEvent.click(getByTestId("checkpoint-decision-chicklet").querySelector("summary")!);
    fireEvent.click(getByTestId("checkpoint-decision-body").querySelector("button")!);
    expect(decisionText()).toContain("Direction sent");
    expect(decisionText()).toContain("Keep the file");
  });

  it("stays a non-expandable pill when subject and grant are absent", () => {
    const meta: CheckpointDecisionMeta = {
      checkpoint_id: "chk-4",
      kind: "tool_approval",
      status: "approved",
    };
    const { getByTestId, queryByTestId } = render(() => (
      <CheckpointDecisionChicklet meta={meta} />
    ));
    const el = getByTestId("checkpoint-decision-chicklet");
    expect(el.tagName).toBe("DIV");
    expect(el.textContent).toContain("Allowed");
    expect(queryByTestId("checkpoint-decision-subject")).toBeNull();
    expect(queryByTestId("checkpoint-decision-body")).toBeNull();
  });

  it("renders tool-card-like structured facts in expanded body", () => {
    const meta: CheckpointDecisionMeta = {
      checkpoint_id: "chk-facts",
      kind: "tool_approval",
      status: "approved",
      tool: "command",
      subject: "cargo build --release",
      location: "local repo → target/release",
      causing_command: "make all",
      grant_title: "Allow for this chat",
      grant_scope: "chat",
    };
    const { getByTestId } = render(() => (
      <CheckpointDecisionChicklet meta={meta} entryKey="checkpoint:facts" />
    ));
    const el = getByTestId("checkpoint-decision-chicklet") as HTMLDetailsElement;
    expect(el.tagName).toBe("DETAILS");
    fireEvent.click(el.querySelector("summary")!);
    expect(el.open).toBe(true);

    const body = getByTestId("checkpoint-decision-body");
    expect(body.querySelector(".den-tool-part-card-facts")).toBeTruthy();
    expect(getByTestId("checkpoint-decision-body-status").textContent).toBe("Allowed");
    expect(getByTestId("checkpoint-decision-body-tool").textContent).toBe("command");
    expect(getByTestId("checkpoint-decision-body-action").textContent).toBe("cargo build --release");
    expect(getByTestId("checkpoint-decision-body-location").textContent).toBe("local repo → target/release");
    expect(getByTestId("checkpoint-decision-body-causing").textContent).toBe("make all");
    expect(getByTestId("checkpoint-decision-body-grant").textContent).toBe("Allow for this chat · chat");
  });

  it("renders pending approval chicklet with pending tone, open marker testid, and review button", () => {
    const meta: CheckpointDecisionMeta = {
      checkpoint_id: "chk-pending",
      kind: "tool_approval",
      status: "pending",
      tool: "command",
      subject: "npm publish",
    };
    const { getByTestId, container } = render(() => (
      <CheckpointDecisionChicklet
        meta={meta}
        sessionId="s-test"
        entryKey="checkpoint:pending"
      />
    ));
    const el = getByTestId("checkpoint-decision-chicklet") as HTMLDetailsElement;
    expect(el.getAttribute("data-status")).toBe("pending");
    expect(el.textContent).toContain("Needs approval");
    expect(container.querySelector(".den-decision")?.getAttribute("data-tone")).toBe("pending");

    // Open marker testid is present for transcript anchor discovery
    const openMarker = container.querySelector('[data-testid="checkpoint-open-marker"]');
    expect(openMarker).toBeTruthy();
    expect(openMarker?.getAttribute("data-checkpoint-id")).toBe("chk-pending");

    // Expand chicklet and click review in composer
    fireEvent.click(el.querySelector("summary")!);
    expect(el.open).toBe(true);

    const reviewBtn = getByTestId("checkpoint-decision-review-dock");
    expect(reviewBtn).toBeTruthy();
    expect(reviewBtn.textContent).toBe("Review in composer");

    fireEvent.click(reviewBtn);
    expect(approvalReviewTarget()).toEqual({
      sessionId: "s-test",
      checkpointId: "chk-pending",
    });
  });
});
