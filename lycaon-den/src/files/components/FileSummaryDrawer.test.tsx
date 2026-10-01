import { stubClient } from "../../test/client-fixture.ts";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import {
  refreshFileSummariesSetting,
  resetFileSummariesSetting,
  saveFileSummariesEnabled,
} from "../../settings/editor/file-summary-settings.ts";
import { resetFileBriefingLiveForTest } from "./file-briefing-live.ts";
import {
  FileSummaryDrawer,
  resetFileSummaryDrawerForTests,
} from "./FileSummaryDrawer.tsx";
import type {
  FileBriefingSelection,
  FileBriefingTarget,
} from "./file-briefing-target.ts";

const response = {
  target_key: "current-main",
  attempt_id: "11111111-1111-4111-8111-111111111111",
  root_id: "r1",
  path: "src/main.ts",
  presentation: "current" as const,
  status: "complete" as const,
    preview: {
      language: "typescript",
      line_count: 20,
    },
  locations: [],
  sections: [
    { kind: "purpose" as const, text: "Starts the application." },
  ],
  fallback_text: "",
  truncated: false,
  source_sha256: "sha-main",
  updated_at: "2026-08-22T00:00:00Z",
};

const currentSelection = {
  availability: "ready" as const,
  target: {
    root_id: "r1",
    path: "src/main.ts",
    presentation: "current" as const,
  },
  displayPath: "src/main.ts",
  unavailableReason: null,
  contextLabel: null,
};

function readySelection(path: string): FileBriefingSelection {
  return {
    availability: "ready",
    target: { root_id: "r1", path, presentation: "current" },
    displayPath: path,
    unavailableReason: null,
    contextLabel: null,
  };
}

describe("FileSummaryDrawer", () => {
  beforeEach(async () => {
    resetFileSummaryDrawerForTests();
    resetFileBriefingLiveForTest();
    resetFileSummariesSetting();
    await refreshFileSummariesSetting({
      getFileSummariesSettings: async () => ({ enabled: true }),
    } as LycaonClient);
  });
  afterEach(() => {
    cleanup();
    registerNoticePublisher(null);
  });

  it("does no summary work until the user opens it", async () => {
    const getFileBriefing = vi.fn().mockRejectedValue(
      new LycaonApiError("missing", 404, "file_briefing_not_found"),
    );
    const requestFileBriefing = vi.fn().mockResolvedValue(response);
    const client = stubClient({ getFileBriefing, requestFileBriefing });

    render(() => (
      <FileSummaryDrawer
        projectId="p1"
        client={client}
        selection={currentSelection}
        onOpenLocation={() => {}}
      />
    ));

    await Promise.resolve();
    expect(getFileBriefing).not.toHaveBeenCalled();
    expect(requestFileBriefing).not.toHaveBeenCalled();

    fireEvent.click(screen.getByTestId("file-summary-toggle"));
    await waitFor(() => expect(requestFileBriefing).toHaveBeenCalledTimes(1));
    expect(
      screen.getByTestId("file-summary-scroll").getAttribute("data-den-scrollport"),
    ).toBe("y");
    expect(getFileBriefing).toHaveBeenCalledTimes(1);
    expect(await screen.findByText("Starts the application.")).toBeTruthy();
  });

  it("shows a stable loading state when there is no prior summary", async () => {
    let resolveSummary: ((value: typeof response) => void) | undefined;
    const client = stubClient({
      getFileBriefing: vi.fn(
        () => new Promise<typeof response>((resolve) => {
          resolveSummary = resolve;
        }),
      ),
      requestFileBriefing: vi.fn(),
    });

    render(() => (
      <FileSummaryDrawer
        projectId="p1"
        client={client}
        selection={currentSelection}
        onOpenLocation={() => {}}
      />
    ));
    fireEvent.click(screen.getByTestId("file-summary-toggle"));

    expect(await screen.findByText("Summarizing this file…")).toBeTruthy();
    expect(screen.getByRole("button", { name: "src/main.ts" })).toBeTruthy();
    expect(screen.getByTestId("file-summary-scroll").getAttribute("aria-busy")).toBe(
      "true",
    );
    resolveSummary?.(response);
    expect(await screen.findByText("Starts the application.")).toBeTruthy();
  });

  it("hides the pull-up and stops requests when File summaries are off", async () => {
    const client = stubClient({
      getFileBriefing: vi.fn(),
      requestFileBriefing: vi.fn(),
      updateFileSummariesSettings: vi.fn().mockResolvedValue({ enabled: false }),
    });
    await saveFileSummariesEnabled(client, false);

    render(() => (
      <FileSummaryDrawer
        projectId="p1"
        client={client}
        selection={currentSelection}
        onOpenLocation={() => {}}
      />
    ));

    expect(screen.queryByTestId("file-summary-toggle")).toBeNull();
    expect(client.getFileBriefing).not.toHaveBeenCalled();
    expect(client.requestFileBriefing).not.toHaveBeenCalled();
  });

  it("opens without a request when no file is eligible", async () => {
    const client = stubClient({
      getFileBriefing: vi.fn(),
      requestFileBriefing: vi.fn(),
    });
    render(() => (
      <FileSummaryDrawer
        projectId="p1"
        client={client}
        selection={{
          availability: "unavailable",
          target: null,
          displayPath: "src/main.ts",
          unavailableReason: "This file does not exist in this version.",
          contextLabel: "Version · Deleted · 2h ago",
        }}
        onOpenLocation={() => {}}
      />
    ));

    fireEvent.click(screen.getByTestId("file-summary-toggle"));
    expect(
      await screen.findByText("This file does not exist in this version."),
    ).toBeTruthy();
    expect(screen.getByText("Version · Deleted · 2h ago")).toBeTruthy();
    expect(client.getFileBriefing).not.toHaveBeenCalled();
    expect(client.requestFileBriefing).not.toHaveBeenCalled();
  });

  it("retains and subdues the last summary across file and summary loading", async () => {
    const nextResponse = {
      ...response,
      target_key: "current-worker",
      path: "src/worker.ts",
      source_sha256: "sha-worker",
      sections: [
        {
          kind: "purpose" as const,
          text: "Runs background work.",
        },
      ],
      updated_at: "2026-08-22T00:00:01Z",
    };
    let resolveNext: ((value: typeof nextResponse) => void) | undefined;
    const getFileBriefing = vi.fn(
      async (_projectId: string, target: FileBriefingTarget) => {
        if (target.path === "src/worker.ts") {
          return await new Promise<typeof nextResponse>((resolve) => {
            resolveNext = resolve;
          });
        }
        return response;
      },
    );
    const client = stubClient({
      getFileBriefing,
      requestFileBriefing: vi.fn(),
    });
    const [selection, setSelection] =
      createSignal<FileBriefingSelection>(currentSelection);
    render(() => (
      <FileSummaryDrawer
        projectId="p1"
        client={client}
        selection={selection()}
        onOpenLocation={() => {}}
      />
    ));
    fireEvent.click(screen.getByTestId("file-summary-toggle"));
    expect(await screen.findByText("Starts the application.")).toBeTruthy();

    setSelection({
      availability: "loading",
      target: null,
      displayPath: "src/worker.ts",
      unavailableReason: "Loading this file…",
      contextLabel: null,
    });
    expect(
      (await screen.findByTestId("file-summary-status")).classList.contains(
        "sr-only",
      ),
    ).toBe(true);
    expect(screen.getByTestId("file-summary-status").textContent).toBe(
      "Loading src/worker.ts…",
    );
    expect(screen.getByText("Starts the application.")).toBeTruthy();
    expect(getFileBriefing).toHaveBeenCalledTimes(1);

    setSelection({
      availability: "ready",
      target: {
        root_id: "r1",
        path: "src/worker.ts",
        presentation: "current",
      },
      displayPath: "src/worker.ts",
      unavailableReason: null,
      contextLabel: null,
    });
    await waitFor(() => expect(getFileBriefing).toHaveBeenCalledTimes(2));
    expect(screen.getByText("Starts the application.")).toBeTruthy();
    expect(screen.getByTestId("file-summary-status").textContent).toBe(
      "Summarizing src/worker.ts…",
    );
    const retained = screen.getByTestId("file-summary-content").parentElement;
    expect(retained?.getAttribute("data-retained")).toBe("true");
    expect(retained?.getAttribute("aria-hidden")).toBe("true");
    expect(retained?.inert).toBe(true);
    expect(screen.getByTestId("file-summary-scroll").getAttribute("aria-busy")).toBe(
      "true",
    );
    expect(screen.getByTestId("file-summary-toggle").textContent).toContain(
      "src/worker.ts",
    );
    expect(screen.getByRole("button", { name: "src/main.ts", hidden: true })).toBeTruthy();

    resolveNext?.(nextResponse);
    expect(await screen.findByText("Runs background work.")).toBeTruthy();
    expect(screen.queryByText("Starts the application.")).toBeNull();
    expect(screen.queryByTestId("file-summary-status")).toBeNull();
    expect(
      screen.getByTestId("file-summary-content").parentElement?.getAttribute(
        "data-retained",
      ),
    ).toBe("false");
    expect(screen.getByTestId("file-summary-scroll").getAttribute("aria-busy")).toBeNull();
  });

  it("replaces retained content with the requested file's failure state", async () => {
    let rejectNext: ((reason: Error) => void) | undefined;
    const getFileBriefing = vi.fn(
      async (_projectId: string, target: FileBriefingTarget) => {
        if (target.path === "src/worker.ts") {
          return await new Promise<typeof response>((_resolve, reject) => {
            rejectNext = reject;
          });
        }
        return response;
      },
    );
    const client = stubClient({ getFileBriefing, requestFileBriefing: vi.fn() });
    const [selection, setSelection] =
      createSignal<FileBriefingSelection>(currentSelection);
    render(() => (
      <FileSummaryDrawer
        projectId="p1"
        client={client}
        selection={selection()}
        onOpenLocation={() => {}}
      />
    ));
    fireEvent.click(screen.getByTestId("file-summary-toggle"));
    expect(await screen.findByText("Starts the application.")).toBeTruthy();

    setSelection(readySelection("src/worker.ts"));
    await waitFor(() => expect(getFileBriefing).toHaveBeenCalledTimes(2));
    expect(screen.getByText("Starts the application.")).toBeTruthy();
    rejectNext?.(new Error("Could not reach the summarizer."));

    expect(await screen.findByRole("button", { name: "Summarize current file" })).toBeTruthy();
    expect(screen.queryByText("Could not reach the summarizer.")).toBeNull();
    expect(screen.queryByText("Starts the application.")).toBeNull();
    expect(screen.getByRole("button", { name: "src/worker.ts" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Summarize current file" })).toBeTruthy();
    expect(screen.getByTestId("file-summary-scroll").getAttribute("aria-busy")).toBeNull();
  });

  it("retains a truncated-only panel while the next file resolves", async () => {
    const truncated = {
      ...response,
      sections: [],
      fallback_text: "",
      truncated: true,
    };
    const nextResponse = {
      ...response,
      target_key: "current-worker",
      path: "src/worker.ts",
      source_sha256: "sha-worker",
      sections: [
        { kind: "purpose" as const, text: "Runs background work." },
      ],
      updated_at: "2026-08-22T00:00:01Z",
    };
    let resolveNext: ((value: typeof nextResponse) => void) | undefined;
    const getFileBriefing = vi.fn(
      async (_projectId: string, target: FileBriefingTarget) => {
        if (target.path === "src/worker.ts") {
          return await new Promise<typeof nextResponse>((resolve) => {
            resolveNext = resolve;
          });
        }
        return truncated;
      },
    );
    const [selection, setSelection] =
      createSignal<FileBriefingSelection>(currentSelection);
    render(() => (
      <FileSummaryDrawer
        projectId="p1"
        client={stubClient({ getFileBriefing, requestFileBriefing: vi.fn() })}
        selection={selection()}
        onOpenLocation={() => {}}
      />
    ));
    fireEvent.click(screen.getByTestId("file-summary-toggle"));
    expect(
      await screen.findByText("Summary shortened to fit the interactive budget."),
    ).toBeTruthy();

    setSelection(readySelection("src/worker.ts"));
    await waitFor(() => expect(getFileBriefing).toHaveBeenCalledTimes(2));
    expect(
      screen.getByText("Summary shortened to fit the interactive budget."),
    ).toBeTruthy();
    expect(
      screen.getByTestId("file-summary-content").parentElement?.getAttribute(
        "data-retained",
      ),
    ).toBe("true");

    resolveNext?.(nextResponse);
    expect(await screen.findByText("Runs background work.")).toBeTruthy();
    expect(
      screen.queryByText("Summary shortened to fit the interactive budget."),
    ).toBeNull();
  });

  it("clears settled content immediately for an unavailable selection", async () => {
    const client = stubClient({
      getFileBriefing: vi.fn().mockResolvedValue(response),
      requestFileBriefing: vi.fn(),
    });
    const [selection, setSelection] =
      createSignal<FileBriefingSelection>(currentSelection);
    render(() => (
      <FileSummaryDrawer
        projectId="p1"
        client={client}
        selection={selection()}
        onOpenLocation={() => {}}
      />
    ));
    fireEvent.click(screen.getByTestId("file-summary-toggle"));
    expect(await screen.findByText("Starts the application.")).toBeTruthy();

    setSelection({
      availability: "unavailable",
      target: null,
      displayPath: "image.png",
      unavailableReason: "File summaries are available for readable text files.",
      contextLabel: null,
    });

    expect(
      await screen.findByText("File summaries are available for readable text files."),
    ).toBeTruthy();
    expect(screen.queryByText("Starts the application.")).toBeNull();
    expect(screen.queryByTestId("file-summary-status")).toBeNull();
  });

  it("never lets a superseded response replace the latest file", async () => {
    const workerResponse = {
      ...response,
      target_key: "current-worker",
      path: "src/worker.ts",
      source_sha256: "sha-worker",
      sections: [
        { kind: "purpose" as const, text: "Runs background work." },
      ],
      updated_at: "2026-08-22T00:00:01Z",
    };
    const finalResponse = {
      ...response,
      target_key: "current-final",
      path: "src/final.ts",
      source_sha256: "sha-final",
      sections: [
        { kind: "purpose" as const, text: "Produces final output." },
      ],
      updated_at: "2026-08-22T00:00:02Z",
    };
    let resolveWorker: ((value: typeof workerResponse) => void) | undefined;
    const getFileBriefing = vi.fn(
      async (_projectId: string, target: FileBriefingTarget) => {
        if (target.path === "src/worker.ts") {
          return await new Promise<typeof workerResponse>((resolve) => {
            resolveWorker = resolve;
          });
        }
        if (target.path === "src/final.ts") return finalResponse;
        return response;
      },
    );
    const [selection, setSelection] =
      createSignal<FileBriefingSelection>(currentSelection);
    render(() => (
      <FileSummaryDrawer
        projectId="p1"
        client={stubClient({ getFileBriefing, requestFileBriefing: vi.fn() })}
        selection={selection()}
        onOpenLocation={() => {}}
      />
    ));
    fireEvent.click(screen.getByTestId("file-summary-toggle"));
    expect(await screen.findByText("Starts the application.")).toBeTruthy();

    setSelection(readySelection("src/worker.ts"));
    await waitFor(() => expect(getFileBriefing).toHaveBeenCalledTimes(2));
    setSelection(readySelection("src/final.ts"));
    expect(await screen.findByText("Produces final output.")).toBeTruthy();
    resolveWorker?.(workerResponse);
    await Promise.resolve();

    expect(screen.getByText("Produces final output.")).toBeTruthy();
    expect(screen.queryByText("Runs background work.")).toBeNull();
  });

  it("never retains a summary across project boundaries", async () => {
    const secondResponse = {
      ...response,
      target_key: "other-project-main",
      sections: [
        { kind: "purpose" as const, text: "Belongs to project two." },
      ],
      source_sha256: "sha-project-two",
      updated_at: "2026-08-22T00:00:01Z",
    };
    let resolveSecond: ((value: typeof secondResponse) => void) | undefined;
    const getFileBriefing = vi.fn(async (projectId: string) => {
      if (projectId === "p2") {
        return await new Promise<typeof secondResponse>((resolve) => {
          resolveSecond = resolve;
        });
      }
      return response;
    });
    const [projectId, setProjectId] = createSignal("p1");
    render(() => (
      <FileSummaryDrawer
        projectId={projectId()}
        client={stubClient({ getFileBriefing, requestFileBriefing: vi.fn() })}
        selection={currentSelection}
        onOpenLocation={() => {}}
      />
    ));
    fireEvent.click(screen.getByTestId("file-summary-toggle"));
    expect(await screen.findByText("Starts the application.")).toBeTruthy();

    setProjectId("p2");
    await waitFor(() => expect(getFileBriefing).toHaveBeenCalledTimes(2));
    expect(screen.queryByText("Starts the application.")).toBeNull();
    expect(screen.getByText("Summarizing this file…")).toBeTruthy();
    expect(screen.queryByTestId("file-summary-status")).toBeNull();

    resolveSecond?.(secondResponse);
    expect(await screen.findByText("Belongs to project two.")).toBeTruthy();
  });

  it("uses a manual retry only once", async () => {
    const failed = {
      ...response,
      status: "failed" as const,
      sections: [],
      error: "Could not finish the summary.",
    };
    const getFileBriefing = vi.fn().mockResolvedValue(failed);
    const requestFileBriefing = vi.fn().mockResolvedValue(response);
    const client = stubClient({ getFileBriefing, requestFileBriefing });

    render(() => (
      <FileSummaryDrawer
        projectId="p1"
        client={client}
        selection={currentSelection}
        onOpenLocation={() => {}}
      />
    ));
    const toggle = screen.getByTestId("file-summary-toggle");
    fireEvent.click(toggle);
    fireEvent.click(
      await screen.findByRole("button", { name: "Summarize current file" }),
    );
    await waitFor(() => expect(requestFileBriefing).toHaveBeenCalledTimes(1));

    fireEvent.click(toggle);
    fireEvent.click(toggle);
    await waitFor(() => expect(getFileBriefing).toHaveBeenCalledTimes(2));
    expect(requestFileBriefing).toHaveBeenCalledTimes(1);
  });

  it("reports a failed summary as a notice and keeps the summarize action", async () => {
    const store = createNoticeStore();
    registerNoticePublisher(store);
    const failed = {
      ...response,
      status: "failed" as const,
      sections: [],
      error: "Could not finish the summary.",
    };
    const client = stubClient({ getFileBriefing: vi.fn().mockResolvedValue(failed) });
    render(() => (
      <FileSummaryDrawer
        projectId="p1"
        client={client}
        selection={currentSelection}
        onOpenLocation={() => {}}
      />
    ));
    fireEvent.click(screen.getByTestId("file-summary-toggle"));
    expect(await screen.findByRole("button", { name: "Summarize current file" })).toBeTruthy();
    await waitFor(() => {
      const rows = selectProjectNoticeGroups(store.index()).find((group) => group.projectId === "p1")?.notices ?? [];
      expect(rows).toMatchObject([{ code: "file_summary_failed" }]);
    });
    expect(screen.queryByText("Could not finish the summary.")).toBeNull();
  });
});
