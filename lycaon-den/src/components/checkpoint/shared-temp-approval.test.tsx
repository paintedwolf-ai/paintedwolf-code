import { render } from "@solidjs/testing-library";
import { expect, it, vi } from "vitest";
import { ApprovalCard } from "./ApprovalCard.tsx";
import { sharedTempCheckpoint, sharedTempExchangeCheckpoint } from "./approval-card-test-fixtures.ts";

it("shows every shared location on the same approval", () => {
  const locations = ["/private/tmp", "/private/var/tmp"];
  const view = render(() => (
    <ApprovalCard
      projectId="project"
      checkpoint={sharedTempCheckpoint(locations)}
      onToolApproval={vi.fn()}
      onContentApply={vi.fn()}
    />
  ));

  for (const path of locations) {
    expect(view.container.textContent).toContain(path);
  }
});

it("renders directional targets and explicit exchange folder option on one card", () => {
  const view = render(() => (
    <ApprovalCard
      projectId="project"
      checkpoint={sharedTempExchangeCheckpoint("/private/tmp/incoming.csv", "/private/tmp/export", "/private/tmp")}
      onToolApproval={vi.fn()}
      onContentApply={vi.fn()}
    />
  ));

  expect(view.container.textContent).toContain("Allow this chat to exchange files in system temp?");
  expect(view.container.textContent).toContain("Read /private/tmp/incoming.csv");
  expect(view.container.textContent).toContain("Create, change, and remove files in /private/tmp/export");
});
