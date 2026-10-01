import { formatQuery, lycaonDownload, type JsonRequester, jsonRequest } from "../http.ts";
import type {
  ActiveWorkflowRunResponse,
  WorkflowRun,
  ComposeWorkflowResponse,
  ComposeFromTemplateRequest,
  PersistWorkflowRequest,
  PersistWorkflowResponse,
  WorkflowTemplateSummary,
  WorkflowTemplateListResponse,
  WorkflowSummary,
  WorkflowListResponse,
  WorkflowRunPage,
  StartWorkflowRunRequest,
  WorkflowControlRequest,
  AdvanceWorkflowRunRequest,
  FireWorkflowTransitionRequest,
  ResolveWorkflowDecisionRequest,
  ResolveUserFeedbackRequest,
  ResolveUserSecretRequest,
} from "../types.ts";
import type { BackendConnection } from "../../platform/connection/backend.ts";

export interface WorkflowsClient {
  listWorkflows(opts?: { projectId?: string; sessionId?: string }): Promise<WorkflowSummary[]>;
  composeWorkflow(sessionId: string, manifestYaml: string, opts?: { dryRun?: boolean }): Promise<ComposeWorkflowResponse>;
  listWorkflowTemplates(): Promise<WorkflowTemplateSummary[]>;
  composeWorkflowFromTemplate(sessionId: string, req: ComposeFromTemplateRequest, opts?: { dryRun?: boolean }): Promise<ComposeWorkflowResponse>;
  persistWorkflow(sessionId: string, workflowId: string, req: PersistWorkflowRequest): Promise<PersistWorkflowResponse>;
  listSessionWorkflowRuns(
    sessionId: string,
    opts?: { limit?: number; status?: string; cursor?: string },
  ): Promise<WorkflowRunPage>;
  getActiveWorkflowRun(sessionId: string): Promise<WorkflowRun | null>;
  startWorkflowRun(sessionId: string, req: StartWorkflowRunRequest): Promise<WorkflowRun>;
  exitWorkflowRun(runId: string, req?: WorkflowControlRequest): Promise<WorkflowRun>;
  getWorkflowRun(runId: string): Promise<WorkflowRun>;
  getWorkflowRunReport(runId: string): Promise<{ blob: Blob; filename: string }>;
  pauseWorkflowRun(runId: string, req: WorkflowControlRequest): Promise<WorkflowRun>;
  resumeWorkflowRun(runId: string, req: WorkflowControlRequest): Promise<WorkflowRun>;
  cancelWorkflowRun(runId: string, req: WorkflowControlRequest): Promise<WorkflowRun>;
  advanceWorkflowRun(runId: string, req: AdvanceWorkflowRunRequest): Promise<WorkflowRun>;
  fireWorkflowTransition(runId: string, transitionId: string, req: FireWorkflowTransitionRequest): Promise<WorkflowRun>;
  resolveWorkflowDecision(
    runId: string,
    phaseId: string,
    req: ResolveWorkflowDecisionRequest,
  ): Promise<WorkflowRun>;
  resolveWorkflowFeedback(
    runId: string,
    phaseId: string,
    req: ResolveUserFeedbackRequest,
  ): Promise<WorkflowRun>;
  resolveWorkflowSecret(
    runId: string,
    phaseId: string,
    req: ResolveUserSecretRequest,
  ): Promise<WorkflowRun>;
}

export function createWorkflowsClient(j: JsonRequester, connection: BackendConnection): WorkflowsClient {
  return {
    listWorkflows: async (opts) => {
      const res = await j<WorkflowListResponse>(
        `/v1/workflows${formatQuery({
          project_id: opts?.projectId,
          session_id: opts?.sessionId,
        })}`,
      );
      return res.workflows;
    },
    composeWorkflow: (sessionId, manifestYaml, opts) =>
      j(
        `/v1/sessions/${sessionId}/workflows/compose${formatQuery({
          dry_run: opts?.dryRun,
        })}`,
        { method: "POST", body: manifestYaml, headers: { "Content-Type": "application/yaml" } },
      ),
    listWorkflowTemplates: async () => {
      const res = await j<WorkflowTemplateListResponse>("/v1/workflow-templates");
      return res.templates;
    },
    composeWorkflowFromTemplate: (sessionId, req, opts) =>
      j(
        `/v1/sessions/${sessionId}/workflows/compose-from-template${formatQuery({
          dry_run: opts?.dryRun,
        })}`,
        jsonRequest("POST", req),
      ),
    persistWorkflow: (sessionId, workflowId, req) =>
      j(
        `/v1/sessions/${sessionId}/workflows/${workflowId}/persist`,
        jsonRequest("POST", req),
      ),
    listSessionWorkflowRuns: (sessionId, opts) =>
      j(
        `/v1/sessions/${sessionId}/workflow-runs${formatQuery({
          limit: opts?.limit,
          status: opts?.status,
          cursor: opts?.cursor,
        })}`,
      ),
    getActiveWorkflowRun: async (sessionId) => {
      const res = await j<ActiveWorkflowRunResponse>(`/v1/sessions/${sessionId}/workflow-runs/active`);
      return res.run;
    },
    startWorkflowRun: (sessionId, req) =>
      j(`/v1/sessions/${sessionId}/workflow-runs`, jsonRequest("POST", req)),
    exitWorkflowRun: (runId, req) =>
      j(`/v1/workflow-runs/${runId}/exit`, jsonRequest("POST", req ?? {})),
    getWorkflowRun: (runId) => j(`/v1/workflow-runs/${runId}`),
    getWorkflowRunReport: async (runId) => {
      const download = await lycaonDownload(
        connection,
        `/v1/workflow-runs/${runId}/report`,
        `painted-wolf-code-report-${runId}.pdf`,
      );
      return { blob: download.blob, filename: download.filename };
    },
    pauseWorkflowRun: (runId, req) =>
	  j(`/v1/workflow-runs/${runId}/pause`, jsonRequest("POST", req)),
    resumeWorkflowRun: (runId, req) =>
	  j(`/v1/workflow-runs/${runId}/resume`, jsonRequest("POST", req)),
    cancelWorkflowRun: (runId, req) =>
	  j(`/v1/workflow-runs/${runId}/cancel`, jsonRequest("POST", req)),
    advanceWorkflowRun: (runId, req) =>
	  j(`/v1/workflow-runs/${runId}/advance`, jsonRequest("POST", req)),
    fireWorkflowTransition: (runId, transitionId, req) =>
	  j(`/v1/workflow-runs/${runId}/transitions/${encodeURIComponent(transitionId)}`, jsonRequest("POST", req)),
    resolveWorkflowDecision: (runId, phaseId, req) =>
      j(
        `/v1/workflow-runs/${runId}/decisions/${phaseId}`,
        jsonRequest("POST", req),
      ),
    resolveWorkflowFeedback: (runId, phaseId, req) =>
      j(
        `/v1/workflow-runs/${runId}/feedback/${phaseId}`,
        jsonRequest("POST", req),
      ),
    resolveWorkflowSecret: (runId, phaseId, req) =>
      j(
        `/v1/workflow-runs/${runId}/feedback/${phaseId}/secret`,
        jsonRequest("POST", req),
      ),

  };
}
