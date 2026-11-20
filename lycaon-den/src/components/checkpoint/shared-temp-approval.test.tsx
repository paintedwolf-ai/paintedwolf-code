import { render } from "@solidjs/testing-library";
import { expect, it, vi } from "vitest";
import { ApprovalCard } from "./ApprovalCard.tsx";
import { sharedTempCheckpoint } from "./approval-card-test-fixtures.ts";

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
