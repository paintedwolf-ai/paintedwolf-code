import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { Message, WorkflowRun, WorkflowSummary } from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import { AssistantChatTurn } from "./AssistantChatTurn.tsx";

vi.mock("../../platform/files/save-file.ts", () => ({
  downloadExport: vi.fn(),
}));

import { downloadExport } from "../../platform/files/save-file.ts";

const catalog: WorkflowSummary[] = [
  {
    id: "security-survey",
    version: "1.0.0",
    name: "Security",
    report_enabled: true,
  },
];

const completeRun: WorkflowRun = {
  id: "run-survey",
  session_id: "s1",
  workflow_id: "security-survey",
  workflow_version: "1.0.0",
	revision: 1,
  status: "complete",
  ui: { current_phase_label: "Done", report_available: true },
  current_phase: "done",
  created_at: "t",
  updated_at: "t",
};

function groundedMessage(overrides: Partial<Message> = {}): Message {
  return {
    id: "msg-1",
    role: "assistant",
    content: "Survey complete.",
    created_at: "t",
    kind: "completion_report",
    workflow_run_id: "run-survey",
    completion_report: { scope: "run", surface_id: "implement_synthesis", phase: "report" },
    grounding: {
      traced: true,
    },
    ...overrides,
  } as Message;
}

function seedStore(runs: WorkflowRun[]) {
  const store = createAppStore();
  const sessionId = runs[0]?.session_id ?? "session-1";
  store.actions.setCurrentSession({ id: sessionId, owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "project-1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
  store.actions.setWorkflowState(sessionId, store.state.sessionViewEpoch, {
    workflowRuns: runs,
    workflowCatalog: catalog,
    blueprints: [],
  });
  return store;
}

describe("AssistantChatTurn report download", () => {
  it("shows Download report on a grounded closeout of a report-enabled terminal run", async () => {
    const store = seedStore([completeRun]);
    const blob = new Blob(["%PDF"], { type: "application/pdf" });
    const getWorkflowRunReport = vi
      .fn()
      .mockResolvedValue({ blob, filename: "report.pdf" });
    const client = { getWorkflowRunReport } as never;
    const messages = [groundedMessage()];
    vi.mocked(downloadExport).mockClear();

    render(() => (
      <AssistantChatTurn
        item={{ kind: "assistant", key: "msg-1", text: "Survey complete." }}
        wireRow={() => messages.find((row) => row.id === "msg-1")}
        client={client}
        appStore={store}
      />
    ));

    expect(screen.getByTestId("workflow-download-report")).toBeTruthy();
    fireEvent.click(screen.getByTestId("workflow-download-report"));
    await waitFor(() => {
      expect(getWorkflowRunReport).toHaveBeenCalledWith("run-survey");
      expect(downloadExport).toHaveBeenCalledWith(blob, "report.pdf");
    });
  });

  it("hides Download report when the run is still running", () => {
    const store = seedStore([{ ...completeRun, status: "running", ui: { current_phase_label: "Done", report_available: false } }]);
    const messages = [groundedMessage()];

    render(() => (
      <AssistantChatTurn
        item={{ kind: "assistant", key: "msg-1", text: "Working…" }}
        wireRow={() => messages.find((row) => row.id === "msg-1")}
        client={{ getWorkflowRunReport: vi.fn() } as never}
        appStore={store}
      />
    ));
    expect(screen.queryByTestId("workflow-download-report")).toBeNull();
  });

  it("hides Download report without grounding", () => {
    const store = seedStore([completeRun]);
    const messages = [groundedMessage({ grounding: undefined })];

    render(() => (
      <AssistantChatTurn
        item={{ kind: "assistant", key: "msg-1", text: "Draft" }}
        wireRow={() => messages.find((row) => row.id === "msg-1")}
        client={{ getWorkflowRunReport: vi.fn() } as never}
        appStore={store}
      />
    ));
    expect(screen.queryByTestId("workflow-download-report")).toBeNull();
  });

  // A phase report is a phase's deliverable, not the run's. The run's own PDF
  // is addressed from the report the declared report phase produced.
  it("hides Download report on a phase-scoped report", () => {
    const store = seedStore([completeRun]);
    const messages = [
      groundedMessage({
        completion_report: { scope: "phase", phase: "execute" },
      } as Partial<Message>),
    ];

    render(() => (
      <AssistantChatTurn
        item={{ kind: "assistant", key: "msg-1", text: "Phase done." }}
        wireRow={() => messages.find((row) => row.id === "msg-1")}
        client={{ getWorkflowRunReport: vi.fn() } as never}
        appStore={store}
      />
    ));
    expect(screen.queryByTestId("workflow-download-report")).toBeNull();
  });

  // A report that states no scope belongs to no scope, so neither route claims
  // it — the record, not the reader, decides what it is.
  it("hides Download report when the record states no scope", () => {
    const store = seedStore([completeRun]);
    const messages = [groundedMessage({ completion_report: undefined } as Partial<Message>)];

    render(() => (
      <AssistantChatTurn
        item={{ kind: "assistant", key: "msg-1", text: "Survey complete." }}
        wireRow={() => messages.find((row) => row.id === "msg-1")}
        client={{ getWorkflowRunReport: vi.fn() } as never}
        appStore={store}
      />
    ));
    expect(screen.queryByTestId("workflow-download-report")).toBeNull();
  });
});

describe("AssistantChatTurn ordinary completion", () => {
  it.each(["session", "phase"] as const)("never exposes a document for %s scope", (scope) => {
    const messages = [groundedMessage({ completion_report: { scope } })];
    render(() => (
      <AssistantChatTurn
        item={{ kind: "assistant", key: "msg-1", text: "Done." }}
        wireRow={() => messages.find((row) => row.id === "msg-1")}
        sessionId="s1"
        appStore={seedStore([completeRun])}
        client={{ getWorkflowRunReport: vi.fn() } as never}
      />
    ));
    expect(screen.queryByTestId("workflow-download-report")).toBeNull();
  });
});

describe("AssistantChatTurn agent note", () => {
  it("renders Note badge and citation chicklet for a grounded agent_note", () => {
    const messages = [
      groundedMessage({
        kind: "agent_note",
        completion_report: undefined,
        content: "Auth lives in middleware.go.",
        grounding: {
          traced: true,
          cited_evidence: [{ path: "middleware.go", line: 12 }],
          checks: [
            {
              id: "path_citations",
              label: "Path citations",
              status: "passed" as const,
              kind: "citation" as const,
              summary: "1 citation(s) matched",
            },
          ],
        },
        workflow_run_id: undefined,
      }),
    ];

    render(() => (
      <AssistantChatTurn
        item={{
          kind: "assistant",
          key: "msg-1",
          text: "Auth lives in middleware.go.",
        }}
        wireRow={() => messages.find((row) => row.id === "msg-1")}
        projectId="p1"
      />
    ));

    const badge = screen.getByTestId("agent-note-badge");
    expect(badge).toBeTruthy();
    expect(badge.getAttribute("aria-label")).toBe("Note");
    expect(badge.textContent).toBe("Note");
    expect(screen.getByTestId("citation-evidence-chicklet")).toBeTruthy();
    expect(screen.queryByTestId("workflow-download-report")).toBeNull();
  });

  it("leaves a normal assistant row without the Note badge", () => {
    const messages = [groundedMessage()];

    render(() => (
      <AssistantChatTurn
        item={{ kind: "assistant", key: "msg-1", text: "Survey complete." }}
        wireRow={() => messages.find((row) => row.id === "msg-1")}
      />
    ));

    expect(screen.queryByTestId("agent-note-badge")).toBeNull();
    expect(screen.getByTestId("transcript-article-assistant")).toBeTruthy();
  });
});

describe("AssistantChatTurn prose citation links", () => {
  it("linkifies a cited path mentioned in the answer prose", () => {
    const messages = [
      groundedMessage({
        content: "The guard lives in `src/foo.ts` now.",
        grounding: {
          traced: true,
          cited_evidence: [{ path: "src/foo.ts", line: 42 }],
        },
      }),
    ];

    const { container } = render(() => (
      <AssistantChatTurn
        item={{
          kind: "assistant",
          key: "msg-1",
          text: "The guard lives in `src/foo.ts` now.",
        }}
        wireRow={() => messages.find((row) => row.id === "msg-1")}
        projectId="p1"
      />
    ));

    const button = container.querySelector(
      ".assistant-prose .den-source-path-link",
    );
    expect(button).toBeTruthy();
    expect(button?.getAttribute("data-den-source-path")).toBe("src/foo.ts");
    expect(button?.getAttribute("data-den-source-line")).toBe("42");
  });

  it("keeps prose mentions plain when the cited path is not openable", () => {
    const messages = [
      groundedMessage({
        content: "See `src/dir` for the layout.",
        grounding: {
          traced: true,
          cited_evidence: [{ path: "src/dir", openable: false }],
        },
      }),
    ];

    const { container } = render(() => (
      <AssistantChatTurn
        item={{ kind: "assistant", key: "msg-1", text: "See `src/dir` for the layout." }}
        wireRow={() => messages.find((row) => row.id === "msg-1")}
        projectId="p1"
      />
    ));

    expect(
      container.querySelector(".assistant-prose .den-source-path-link"),
    ).toBeNull();
  });

  it("keeps uncited paths plain without a durable navigation ref", () => {
    const messages = [
      groundedMessage({
        content: "Never hand-edit `docs/openapi.yaml`.",
        grounding: { traced: false, host_assembled: true },
      }),
    ];

    const { container } = render(() => (
      <AssistantChatTurn
        item={{
          kind: "assistant",
          key: "msg-1",
          text: "Never hand-edit `docs/openapi.yaml`.",
        }}
        wireRow={() => messages.find((row) => row.id === "msg-1")}
        projectId="p1"
      />
    ));

    expect(
      container.querySelector(".assistant-prose .den-source-path-link"),
    ).toBeNull();
  });

  it("opens a standalone path fence from durable host navigation metadata", () => {
    const path = "lycaon/config/packs/painted-wolf/security/host/detection-packs";
    const content = `Where the rules live\n\n\`\`\`\n${path}/\n\`\`\``;
    const messages = [
      groundedMessage({
        content,
        grounding: { traced: false, host_assembled: true },
        navigation_refs: [
          {
            id: "ref-1", syntax: "fence", status: "resolved", explicit: true, mention: `${path}/`,
            project_id: "p1",
            root_id: "root-1",
            path,
            entry_kind: "folder",
          },
        ],
      }),
    ];

    const { container } = render(() => (
      <AssistantChatTurn
        item={{ kind: "assistant", key: "msg-1", text: content }}
        wireRow={() => messages.find((row) => row.id === "msg-1")}
        projectId="p1"
      />
    ));

    const button = container.querySelector(
      ".assistant-prose .den-source-path-block .den-source-path-link",
    );
    expect(button?.getAttribute("data-den-source-path")).toBe(path);
    expect(button?.getAttribute("data-den-project-root-id")).toBe("root-1");
    expect(button?.getAttribute("data-den-project-path-kind")).toBe("folder");
  });
});
