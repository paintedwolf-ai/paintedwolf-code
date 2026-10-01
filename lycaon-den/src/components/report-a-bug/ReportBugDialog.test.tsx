import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { ISSUES_URL } from "../../../shared/brand.ts";
import type { BackendConnection } from "../../platform/connection/backend.ts";
import { ReportBugDialog } from "./ReportBugDialog.tsx";

const reveal = vi.hoisted(() => vi.fn(async () => ({ status: "revealed" as const })));
vi.mock("../../platform/runtime.ts", async (original) => ({
  ...await original<typeof import("../../platform/runtime.ts")>(),
  isTauriRuntime: () => true, tauriPlatform: () => "macos",
}));
vi.mock("../../platform/files/reveal-in-file-manager.ts", async (original) => ({ ...await original<typeof import("../../platform/files/reveal-in-file-manager.ts")>(), revealInFileManager: reveal }));

const connection = {
  baseUrl: "http://127.0.0.1:8787",
  apiToken: "t",
} as BackendConnection;

describe("ReportBugDialog", () => {
  it("shows explain copy on open", () => {
    render(() => <ReportBugDialog open onClose={() => undefined} />);
    expect(screen.getByTestId("report-bug-explain").textContent).toMatch(
      /nothing is sent automatically/i,
    );
    expect(screen.getByTestId("report-bug-save").textContent).toMatch(
      /Save report bundle/i,
    );
  });

  it("after save shows path, Reveal, and Open GitHub Issues", async () => {
    const save = vi.fn(async () => ({
      kind: "saved" as const,
      path: "/tmp/report-bundle.zip",
    }));
    const openIssues = vi.fn(async () => true);

    render(() => (
      <ReportBugDialog
        open
        onClose={() => undefined}
        save={save}
        openIssues={openIssues}
        getConnection={() => connection}
      />
    ));

    fireEvent.click(screen.getByTestId("report-bug-save"));

    await waitFor(() => {
      expect(screen.getByTestId("report-bug-path").textContent).toBe(
        "/tmp/report-bundle.zip",
      );
    });
    expect(screen.getByTestId("report-bug-filename").textContent).toMatch(
      /Do not attach/i,
    );
    expect(screen.getByTestId("open-in-button")).toBeTruthy();
    expect(screen.getByTestId("report-bug-open-issues").textContent).toMatch(
      /GitHub Issues/i,
    );

    fireEvent.click(screen.getByTestId("open-in-button"));
    fireEvent.click(screen.getByTestId("open-in-file-manager"));
    await waitFor(() => {
      expect(reveal).toHaveBeenCalledWith("/tmp/report-bundle.zip", [
        "/tmp/report-bundle.zip",
      ]);
    });

    fireEvent.click(screen.getByTestId("report-bug-open-issues"));
    await waitFor(() => {
      expect(openIssues).toHaveBeenCalledWith(ISSUES_URL);
    });
  });

  it("explains where to find a browser download instead of offering an inert reveal", async () => {
    render(() => <ReportBugDialog open onClose={() => undefined}
      save={async () => ({ kind: "saved", path: "report.zip" })}
      getConnection={() => connection} />);
    fireEvent.click(screen.getByTestId("report-bug-save"));
    await waitFor(() => expect(screen.getByTestId("report-bug-path").textContent).toBe("report.zip"));
    expect(screen.queryByTestId("open-in-button")).toBeNull();
    expect(screen.getByText("Find the report in your browser downloads.")).toBeTruthy();
  });

  it("cancel of the save dialog stays on explain", async () => {
    const save = vi.fn(async () => ({ kind: "cancelled" as const }));
    const openIssues = vi.fn(async () => true);

    render(() => (
      <ReportBugDialog
        open
        onClose={() => undefined}
        save={save}
        openIssues={openIssues}
        getConnection={() => connection}
      />
    ));

    fireEvent.click(screen.getByTestId("report-bug-save"));

    await waitFor(() => {
      expect(screen.getByTestId("report-bug-explain")).toBeTruthy();
    });
    expect(screen.queryByTestId("report-bug-path")).toBeNull();
    expect(openIssues).not.toHaveBeenCalled();
  });
});
