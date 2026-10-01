import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import { describe, expect, it } from "vitest";
import {
  enrichCheckpointDecision,
  checkpointMetaFromPending,
  approvalCausingCommand,
  approvalHeadline,
  approvalIdentity,
  approvalLocationLine,
} from "./approval-display.ts";
import {
  approvalOptionFixture,
  toolApprovalFixture,
} from "./approval-test-fixtures.ts";

describe("approval display", () => {
  it("surfaces a causing command when the subject is a write root, not the argv", () => {
    const payload = toolApprovalFixture({
      tool: "command",
      command: "go mod tidy",
      subject: {
        kind: "write_root_set",
        title: "Allow write access",
        targets: [{ kind: "write_root", label: "/Users/example/go" }],
      },
    });
    expect(approvalCausingCommand(payload)).toBe("go mod tidy");
  });

  it("does not repeat the command when it is already the subject", () => {
    const payload = toolApprovalFixture({ command: "git push origin main" });
    expect(approvalCausingCommand(payload)).toBe("");
  });

  it("surfaces a causing command for a destination set", () => {
    const payload = toolApprovalFixture({
      tool: "network",
      command: "curl https://example.com",
      subject: {
        kind: "destination_set",
        title: "Allow network",
        targets: [{ kind: "destination", label: "example.com" }],
      },
    });
    expect(approvalCausingCommand(payload)).toBe("curl https://example.com");
  });

  it("composes secret identity from kind and location", () => {
    const payload = toolApprovalFixture({
      tool: "model_request",
      subject: {
        kind: "secret",
        title: "Credential detected before sending this model request",
        targets: [{ kind: "secret", label: "GitHub Personal Access Token" }],
      },
      presentation: {
        location: {
          origin: ".env.local:7",
          destination: "Fireworks",
          origin_kind: "file",
          destination_kind: "model_provider",
          path: ".env.local",
          line: 7,
        },
      },
    });
    expect(approvalLocationLine(payload)).toBe(".env.local:7 → Fireworks");
    expect(approvalIdentity(payload)).toBe(
      "GitHub Personal Access Token · .env.local:7 → Fireworks",
    );
  });

  it("uses explanation when title and args are absent", () => {
    const payload = toolApprovalFixture({ tool: "", title: "Inspect working tree." });
    expect(approvalHeadline(payload)).toBe("Inspect working tree.");
  });

  it("enriches thin decision meta from the sibling tool call", () => {
    const enriched = enrichCheckpointDecision(
      {
        checkpoint_id: "cp-1",
        kind: "tool_approval",
        status: "approved",
      },
      { id: "call-1", name: "command", args: { command: "git push --force" } },
    );
    expect(enriched.tool).toBe("command");
    expect(enriched.subject).toBe("command: git push --force");
  });

  it("preserves host-stamped grant fields and causing_command", () => {
    const meta = {
      checkpoint_id: "cp-grant",
      kind: "tool_approval" as const,
      status: "approved" as const,
      tool: "command",
      subject: "/Users/example/go",
      causing_command: "go mod tidy",
      grant_scope: "chat" as const,
      grant_title: "Allow for this chat",
      grant_ids: ["host-opaque-id"],
    };
    const enriched = enrichCheckpointDecision(meta, {
      id: "call-g",
      name: "command",
      args: { command: "go mod tidy" },
    });
    expect(enriched.grant_scope).toBe("chat");
    expect(enriched.grant_title).toBe("Allow for this chat");
    expect(enriched.grant_ids).toEqual(["host-opaque-id"]);
    expect(enriched.causing_command).toBe("go mod tidy");
  });

  it("enriches content_apply subject from file_edit path", () => {
    const enriched = enrichCheckpointDecision(
      {
        checkpoint_id: "cp-2",
        kind: "content_apply",
        status: "approved",
      },
      { id: "call-2", name: "edit", args: { path: "src/a.ts" } },
      { content: "", tool: "edit", file_edit_preview: fileEditPreviewFixture({ path: "src/a.ts", after: "x" }) },
    );
    expect(enriched.tool).toBe("edit");
    expect(enriched.subject).toBe("src/a.ts");
  });

  it("extracts rich metadata from pending tool approval checkpoints", () => {
    const fixture = toolApprovalFixture({ command: "git push" });
    fixture.plan.presentation.location = {
      origin: "local",
      destination: "github.com",
      origin_kind: "file",
      destination_kind: "service",
    };
    fixture.plan.options = [
      approvalOptionFixture({
        id: "opt-face",
        title: "Allow for this chat",
        scope: "chat",
        reask_when: "never",
      }),
    ];
    fixture.plan.recommended_option_id = "opt-face";

    const meta = checkpointMetaFromPending({
      checkpointId: "cp-pending-1",
      sessionId: "s1",
      kind: "tool_approval",
      status: "pending",
      issuedAt: "t",
      tool_approval: fixture,
    });

    expect(meta).toEqual({
      checkpoint_id: "cp-pending-1",
      kind: "tool_approval",
      status: "pending",
      tool: "command",
      subject: "git push",
      location: "local → github.com",
      grant_title: "Allow for this chat",
      grant_scope: "chat",
    });
  });

  it("extracts path and tool from pending content apply checkpoints", () => {
    const meta = checkpointMetaFromPending({
      checkpointId: "cp-pending-2",
      sessionId: "s1",
      kind: "content_apply",
      status: "pending",
      issuedAt: "t",
      content_apply: {
        tool: "file_edit",
        path: "src/main.rs",
        after: "fn main() {}",
        hunks: [],
      },
    });

    expect(meta).toEqual({
      checkpoint_id: "cp-pending-2",
      kind: "content_apply",
      status: "pending",
      tool: "file_edit",
      subject: "src/main.rs",
    });
  });
});
