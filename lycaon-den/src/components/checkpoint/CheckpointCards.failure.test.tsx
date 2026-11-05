import { expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { LycaonApiError } from "../../api/http.ts";
import { NoticeReporterProvider } from "../../notices/notice-reporter.tsx";
import { createAppStore } from "../../store/app-state.ts";
import { mockProjectsStore } from "../../test/projects-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import { checkpoint, contentCheckpoint } from "./approval-card-test-fixtures.ts";
import { CheckpointCards } from "./CheckpointCards.tsx";

function mountCards(cards: PendingCheckpoint[], client: LycaonClient) {
  const appStore = createAppStore();
  appStore.actions.setCurrentSession({
    id: "session-1", project_id: "project", owner_person_id: "person",
    workspace_path: "/fixture", posture: "build", status: "busy",
    created_at: "t", activity_at: "t", updated_at: "t",
  });
  appStore.actions.setPendingCheckpoints("session-1", appStore.state.sessionViewEpoch, appStore.state.checkpointEventEpoch, cards);
  const projects = mockProjectsStore([{ id: "project", roots: [{ id: "root", path: "/fixture", is_primary: true }] }] as never);
  render(() => (
    <NoticeReporterProvider reporter={{ reportError: vi.fn(), publish: vi.fn() }}>
      <CheckpointCards appStore={appStore} client={client} sessionId="session-1" projectDir="/fixture" projects={projects} />
    </NoticeReporterProvider>
  ));
}

it("submits an applied file change as that checkpoint's decision", async () => {
  const resolveCheckpoint = vi.fn().mockResolvedValue(undefined);
  mountCards([contentCheckpoint()], stubClient({ resolveCheckpoint }));
  fireEvent.click(screen.getByTestId("content-apply-approve"));
  await waitFor(() => expect(resolveCheckpoint).toHaveBeenCalledOnce());
  expect(resolveCheckpoint.mock.calls[0]?.[1]).toBe("content-checkpoint-1");
  expect(resolveCheckpoint.mock.calls[0]?.[2]).toMatchObject({ kind: "content_apply", decision: "approve" });
});

it("removes the path's review rule before applying when review is skipped for good", async () => {
  const resolveCheckpoint = vi.fn().mockResolvedValue(undefined);
  const updateReviewSettings = vi.fn().mockResolvedValue({});
  const getReviewSettings = vi.fn().mockResolvedValue({ review_paths: [{ path: "notes.txt" }, { path: "other.txt" }] });
  mountCards([contentCheckpoint()], stubClient({ resolveCheckpoint, updateReviewSettings, getReviewSettings }));
  fireEvent.click(screen.getByTestId("content-apply-skip-face"));
  fireEvent.click(screen.getByTestId("approval-grant-menu").querySelector('[data-offer-id="always"]')!);
  await waitFor(() => expect(resolveCheckpoint).toHaveBeenCalledOnce());
  expect(updateReviewSettings).toHaveBeenCalledWith({ review_paths: [{ path: "other.txt" }] }, "project");
  expect(resolveCheckpoint.mock.calls[0]?.[2]).toMatchObject({ kind: "content_apply", decision: "approve" });
});

it("keeps a failed decision's notice on its queue row after another card takes over", async () => {
  const failure = new LycaonApiError("Fixture message", 422, "approval_capacity_exceeded", {
    title: "Fixture failure title",
    retryable: false,
    scope: "session",
  });
  const resolveCheckpoint = vi.fn().mockRejectedValue(failure);
  mountCards([checkpoint(), { ...checkpoint(), checkpointId: "checkpoint-2" }], stubClient({ resolveCheckpoint }));
  fireEvent.click(screen.getByTestId("approval-approve-primary"));
  await screen.findByTestId("approval-decision-error");
  expect(screen.queryByTestId("checkpoint-queue-error")).toBeNull();
  fireEvent.click(screen.getByTestId("checkpoint-queue-row"));
  const row = await waitFor(() => {
    const failed = screen.getByTestId("checkpoint-queue-row");
    expect(failed.getAttribute("data-checkpoint-id")).toBe("checkpoint-1");
    return failed;
  });
  expect(row.querySelector('[data-testid="checkpoint-queue-error"]')?.textContent).toContain("Fixture failure title");
});

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
