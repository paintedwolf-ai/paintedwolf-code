import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { repoRootDir } from "./connection/host-authority-registry.ts";

const repoRoot = repoRootDir();

describe("decision journey wiring", () => {
  it("plan state gates approval on host WorkflowRun.ui flag", () => {
    const src = readFileSync(
      join(repoRoot, "lycaon-den/src/components/blueprint/chat-blueprint-state.ts"),
      "utf8",
    );
    expect(src).toContain("ui?.human_approval_awaiting");
    expect(src).not.toMatch(/plan-approval-detect|plan_approved/i);
    expect(src).not.toMatch(/message\.content\.includes/);
  });

  it("workflow state gates start proposal on session ui DTO", () => {
    const src = readFileSync(
      join(repoRoot, "lycaon-den/src/components/chatview/chat-workflow-state.ts"),
      "utf8",
    );
    expect(src).toContain("ui?.pending_workflow_start");
  });

  it("ChatView leaves the workflow start proposal to chat workflow state", () => {
    const src = readFileSync(
      join(repoRoot, "lycaon-den/src/components/chatview/ChatView.tsx"),
      "utf8",
    );
    expect(src).not.toContain("ui?.pending_workflow_start");
  });

  it("tool card status derives from tool_result.outcome not content heuristics", () => {
    const src = readFileSync(
      join(repoRoot, "lycaon-den/src/chat/tool/tool-part-model.ts"),
      "utf8",
    );
    expect(src).toContain("tool_result?.outcome");
    expect(src).not.toContain("Rejected:");
    expect(src).not.toMatch(/content\.includes\("Code:/);
  });

  it("taskJobIdFromPart does not scrape completion envelope job_id", () => {
    const src = readFileSync(
      join(repoRoot, "lycaon-den/src/chat/task/task-result-model.ts"),
      "utf8",
    );
    expect(src).not.toContain("parseWorkerCompletionEnvelope");
  });

  it("worker binding uses wire job_id only", () => {
    const src = readFileSync(
      join(repoRoot, "lycaon-den/src/chat/worker/workers-model.ts"),
      "utf8",
    );
    expect(src).toMatch(/taskJobIdFromPart/);
    expect(src).not.toMatch(/prompt\.includes|description\.includes/);
    expect(src).not.toContain("parseWorkerCompletionEnvelope");
    expect(src).toContain("syncWorkerFromTaskToolMessage");
  });

  it("task-card-model binds status from worker queue or tool_result only", () => {
    const src = readFileSync(
      join(repoRoot, "lycaon-den/src/chat/task/task-card-model.ts"),
      "utf8",
    );
    expect(src).not.toContain("parseWorkerCompletionEnvelope");
    expect(src).not.toMatch(/worker-completion-envelope/);
    expect(src).toMatch(/workerRowStatus\(worker\)/);
  });

  it("worker completion display lives in markdown-output not envelope module", () => {
    expect(
      existsSync(
        join(repoRoot, "lycaon-den/src/chat/worker-completion-envelope.ts"),
      ),
    ).toBe(false);
    const markdown = readFileSync(
      join(repoRoot, "lycaon-den/src/chat/markdown/markdown-output.ts"),
      "utf8",
    );
    expect(markdown).toContain("workerTranscriptMarkdownSource");
  });
});
