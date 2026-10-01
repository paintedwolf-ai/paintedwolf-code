import { describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import { toolApprovalFixture } from "../../chat/checkpoint/approval-test-fixtures.ts";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
vi.mock("../../platform/navigation/open-files-surface.ts", () => ({openFilesSurface: vi.fn()}));
import { ApprovalCard } from "./ApprovalCard.tsx";

function destinationSetCheckpoint(hostCount: number): PendingCheckpoint {
  const hosts = Array.from({ length: hostCount }, (_, i) => `host-${i}.example`);
  return {
    checkpointId: "checkpoint-destinations",
    sessionId: "session-destinations",
    kind: "tool_approval",
    status: "pending",
    issuedAt: "2026-08-06T00:00:00Z",
    tool_approval: toolApprovalFixture({
      tool: "network",
      subject: {
        kind: "destination_set",
        title: `Allow network: ${hostCount} configured endpoints`,
        targets: hosts.map((host) => ({ kind: "destination", label: host })),
      },
      presentation: { action: "Use network", tool: "network" },
    }),
  };
}

function renderCard(checkpoint: PendingCheckpoint) {
  return render(() => (
    <ApprovalCard
      projectId="project"
      checkpoint={checkpoint}
      sessionId={checkpoint.sessionId}
      onToolApproval={vi.fn()}
      onContentApply={vi.fn()}
    />
  ));
}

describe("set subjects stay answerable", () => {
  it.each([
    { readPaths: [] },
    { readPaths: ["/fixture/cacert.pem"] },
    { readPaths: ["/fixture/credentials"] },
  ])("summarizes package identity and approved read access $readPaths", ({ readPaths }) => {
    const checkpoint: PendingCheckpoint = {
      checkpointId: "checkpoint-package",
      sessionId: "session-package",
      kind: "tool_approval",
      status: "pending",
      issuedAt: "2026-08-27T00:00:00Z",
      tool_approval: toolApprovalFixture({
        tool: "command",
        command: "npx create-vite@latest demo",
        reasons: ["remote_package_execution"],
        subject: {
          kind: "package_set",
          title: "Run downloaded package code",
          targets: [{
            kind: "package",
            label: "create-vite@6.1.0",
            details: {
              identity_status: "resolved",
              age_days: 7,
              verified_attestation: true,
              ambient_credentials_removed: true,
              protected_reads_denied: true,
              approved_read_paths: readPaths,
              network_scope: "registry_only",
              allowed_hosts: ["registry.npmjs.org"],
            },
          }],
        },
        presentation: { gate: "remote_package_execution" },
      }),
    };
    const view = renderCard(checkpoint);
    const target = view.getByTestId("approval-target-0");
    expect(target.textContent).toContain("create-vite@6.1.0");
    expect(target.textContent).toContain("published 7 days ago");
    expect(target.textContent).toContain("source attestation verified");
    expect(target.textContent).toContain("without inherited credentials");
    expect(target.textContent).toContain(readPaths.length > 0
      ? "other protected reads require approval"
      : "Protected reads require approval");
    for (const path of readPaths) expect(target.textContent).toContain(`Approved read access to ${path}`);
    expect(target.textContent).toContain("Network only to registry.npmjs.org");
  });

  it("makes an unclaimed package identity explicit", () => {
    const checkpoint: PendingCheckpoint = {
      checkpointId: "checkpoint-package-not-found",
      sessionId: "session-package",
      kind: "tool_approval",
      status: "pending",
      issuedAt: "2026-08-27T00:00:00Z",
      tool_approval: toolApprovalFixture({
        tool: "command",
        command: "pip install internal-tool",
        reasons: ["remote_package_execution"],
        subject: {
          kind: "package_set",
          title: "Run downloaded package code",
          targets: [{
            kind: "package",
            label: "internal-tool",
            details: { identity_status: "not_found" },
          }],
        },
        presentation: { gate: "remote_package_execution" },
      }),
    };
    const target = renderCard(checkpoint).getByTestId("approval-target-0");
    expect(target.textContent).toContain("not found in the registry identity index");
    expect(target.textContent).toContain("approval will not be reused");
  });

  it("opens the complete long set in Files while keeping decision controls local", () => {
    const view = renderCard(destinationSetCheckpoint(39));
    expect(view.getByTestId("approval-subject").textContent).toBe("Allow network: 39 configured endpoints");
    expect(view.queryByTestId("approval-targets")).toBeNull();
    fireEvent.click(view.getByRole("button", {name: "Show all 39 destinations in Files"}));
    const request = vi.mocked(openFilesSurface).mock.lastCall?.[0];
    expect(request?.kind).toBe("chat-content");
    if (request?.kind !== "chat-content") throw new Error("Expected a Files document");
    expect(request.document).toMatchObject({kind:"approval",checkpointId:"checkpoint-destinations",pane:"targets"});
    expect(request.document.content.kind).toBe("inline");
    if(request.document.content.kind !== "inline") throw new Error("Expected recorded approval text");
    expect(request.document.content.text).toContain("1. host-0.example");
    expect(request.document.content.text).toContain("39. host-38.example");
    expect(view.getByTestId("approval-approve-primary")).toBeTruthy();
    expect(view.getByTestId("approval-no")).toBeTruthy();
  });

  it("enumerates a short set in place with no disclosure", () => {
    const view = renderCard(destinationSetCheckpoint(3));

    expect(view.queryByTestId("approval-targets-disclosure")).toBeNull();
    expect(view.queryByTestId("approval-subject")).toBeNull();
    expect(view.getAllByTestId(/^approval-target-\d+$/)).toHaveLength(3);
  });

  it("does not approve when Enter opens the complete target list", () => {
    const onToolApproval = vi.fn();
    const checkpoint = destinationSetCheckpoint(39);
    const view = render(() => (
      <ApprovalCard
        projectId="project"
      checkpoint={checkpoint}
        sessionId={checkpoint.sessionId}
        onToolApproval={onToolApproval}
        onContentApply={vi.fn()}
      />
    ));

    const summary = view.getByRole("button", {name: "Show all 39 destinations in Files"});
    summary.focus();
    summary.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
    );

    expect(onToolApproval).not.toHaveBeenCalled();

    // The primary button still resolves through the same key listener.
    const primary = view.getByTestId("approval-approve-primary");
    primary.focus();
    primary.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
    );
    expect(onToolApproval).toHaveBeenCalledWith("approve", {
      optionId: "approve_current_action",
    });
  });

  it("sends large target sets to Files instead of listing them inline", () => {
    const view = renderCard(destinationSetCheckpoint(39));
    expect(view.queryByTestId("approval-targets")).toBeNull();
    expect(view.container.querySelector('[data-testid="virtual-card-list"]')).toBeNull();
    expect(view.getByTestId("approval-approve-primary")).toBeTruthy();
    expect(view.getByTestId("approval-no")).toBeTruthy();
  });
});
