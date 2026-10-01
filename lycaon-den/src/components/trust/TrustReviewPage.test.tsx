import { stubClient } from "../../test/client-fixture.ts";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ProjectTrust } from "../../api/types.ts";
import { projectTrustView, loadProjectTrust, resetProjectTrustForTests } from "../../settings/security/project-trust.ts";
import { TrustReviewPage } from "./TrustReviewPage.tsx";
import { TrustPopoverBody } from "./TrustPopover.tsx";
import { openTrustChange } from "./trust-review-navigation.ts";

vi.mock("./trust-review-navigation.ts", () => ({ openTrustChange: vi.fn() }));
vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: () => null }));

const trust: ProjectTrust = {
  project_id: "project", unread_count: 1, 
  surfaces: [{ id: "agents_md", label: "Instructions", group: "steering", count: 99, items: [], seen: false, applying: true, device_enabled: true, project_enabled: true }],
  review: { id: "review", project_id: "project", changes: [{ id: "change", root_id: "detached", root_label: "Previous folder", path: "AGENTS.md", surface_ids: ["agents_md"], kind: "removed", before: "retained instructions", after: "" }] },
};
beforeEach(resetProjectTrustForTests);

describe("shared trust review", () => {
  it("uses the same files and count in the popup and group before and after acknowledgement", async () => {
    await publishTrust(trust);
    const onOpen = vi.fn();
    render(() => <><TrustPopoverBody trust={projectTrustView("project").value!} onOpen={onOpen} /><TrustReviewPage projectId="project" /></>);
    const count = () => expect(screen.getByTestId("trust-popover-title").textContent).toBe(screen.getByTestId("trust-review-count").textContent);
    count();
    expect(screen.getByTestId("trust-popover-title").textContent).toBe("1 file changed");
    expect(screen.getByTestId("trust-popover-row-agents_md").textContent).not.toContain("99");
    fireEvent.click(screen.getByTestId("trust-popover-row-agents_md"));
    expect(onOpen).toHaveBeenCalledWith();
    await publishTrust({ ...trust, unread_count: 0 });
    count();
    expect(screen.getAllByTestId("trust-review-file")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Review AGENTS.md" }));
    expect(openTrustChange).toHaveBeenCalledWith(trust.review, trust.review.changes[0]);
    expect(screen.queryByText(/Show all changes/)).toBeNull();
  });
  it("updates the popup and group together when the host publishes later changes", async () => {
    await publishTrust(trust);
    render(() => <><TrustPopoverBody trust={projectTrustView("project").value!} /><TrustReviewPage projectId="project" /></>);
    await publishTrust({ ...trust, unread_count: 2, review: { ...trust.review, id: "later", changes: [...trust.review.changes, {
      id: "nested", root_id: "root", root_label: "Current folder", path: "nested/AGENTS.md", surface_ids: ["agents_md"], kind: "added", before: "", after: "new instructions",
    }] } });
    expect(screen.getByTestId("trust-popover-title").textContent).toBe("2 files changed");
    expect(screen.getByTestId("trust-review-count").textContent).toBe("2 files changed");
    expect(screen.getAllByTestId("trust-review-file")).toHaveLength(2);
  });
});

async function publishTrust(value: ProjectTrust): Promise<void> {
  await loadProjectTrust(stubClient({ getProjectTrust: async () => value }), value.project_id);
}
