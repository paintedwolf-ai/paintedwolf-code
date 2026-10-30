import { expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { LycaonApiError } from "../../api/http.ts";
import { NoticeReporterProvider } from "../../notices/notice-reporter.tsx";
import { createAppStore } from "../../store/app-state.ts";
import { mockProjectsStore } from "../../test/projects-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { checkpoint } from "./approval-card-test-fixtures.ts";
import { CheckpointCards } from "./CheckpointCards.tsx";

it("keeps a failed approval pending, shows its recovery on the card, and permits rejection", async () => {
  const appStore = createAppStore();
  const card = checkpoint();
  appStore.actions.setCurrentSession({
    id: "session-1", project_id: "project", owner_person_id: "person",
    workspace_path: "/fixture", posture: "build", status: "busy",
    created_at: "t", activity_at: "t", updated_at: "t",
  });
  appStore.actions.setPendingCheckpoints("session-1", appStore.state.sessionViewEpoch, appStore.state.checkpointEventEpoch, [card]);
  const failure = new LycaonApiError("This approval exceeds the app’s decision capacity. The action is still waiting for your decision.", 422, "approval_capacity_exceeded", {
    title: "Approval could not be applied",
    suggestedAction: "Reject the action or stop the session.",
    retryable: false,
    scope: "session",
  });
  const resolveCheckpoint = vi.fn().mockRejectedValueOnce(failure).mockResolvedValue(undefined);
  const reportError = vi.fn();
  render(() => (
    <NoticeReporterProvider reporter={{ reportError, publish: vi.fn() }}>
      <CheckpointCards appStore={appStore} client={stubClient({ resolveCheckpoint })}
        sessionId="session-1" projectDir="/fixture" projects={mockProjectsStore()} />
    </NoticeReporterProvider>
  ));
  const approve = screen.getByTestId("approval-approve-primary");
  const originalChoice = approve.textContent;
  fireEvent.click(approve);
  const notice = await screen.findByTestId("approval-decision-error");
  expect(notice.getAttribute("data-code")).toBe("approval_capacity_exceeded");
  expect(notice.textContent).toContain("still waiting for your decision");
  expect(notice.textContent).toContain("Reject the action or stop the session");
  expect(screen.getByTestId("tool-approval-card").contains(notice)).toBe(true);
  expect((approve as HTMLButtonElement).disabled).toBe(false);
  expect(approve.textContent).toBe(originalChoice);
  expect(reportError).not.toHaveBeenCalled();
  fireEvent.click(screen.getByTestId("approval-no"));
  await waitFor(() => expect(resolveCheckpoint).toHaveBeenCalledTimes(2));
  expect(resolveCheckpoint.mock.calls[1]?.[2]).toMatchObject({ kind: "tool_approval", action: "reject" });
  await waitFor(() => expect(screen.queryByTestId("approval-decision-error")).toBeNull());
});
