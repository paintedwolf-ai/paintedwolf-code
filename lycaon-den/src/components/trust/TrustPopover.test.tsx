import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { ProjectTrust, TrustSurfaceId } from "../../api/types.ts";
import { TrustPopoverBody } from "./TrustPopover.tsx";
import { trustDotState } from "../../settings/security/trust-model.ts";

function trust(): ProjectTrust {
  const ids: TrustSurfaceId[] = ["agents_md", "skills", "project_settings", "project_mcp", "scan_config", "prompt_overrides", "extension_config", "extension_suggestions"];
  return { project_id: "project", unread_count: 8,
    review: { id: "review", project_id: "project", changes: ids.map(id => ({ id, root_id: "root", root_label: "Root", path: `${id}.yaml`, surface_ids: [id], kind: "added", before: "", after: "content" })) },
    surfaces: ids.map(id => ({ id, label: id, count: 1, items: [], group: "steering", applying: true, device_enabled: true, project_enabled: true, seen: id !== "extension_suggestions" })) };
}

describe("trust popup", () => {
  it("bounds its rows and opens the same complete review from every category", () => {
    const onOpen = vi.fn();
    render(() => <TrustPopoverBody trust={trust()} onOpen={onOpen} />);
    expect(screen.getAllByRole("listitem")).toHaveLength(4);
    const rows = screen.getAllByRole("button");
    expect(rows[0]?.textContent).toContain("agents_md");
    fireEvent.click(rows[0]!);
    expect(onOpen).toHaveBeenCalledWith();
    expect(screen.getByTestId("trust-popover-title").textContent).toBe("8 files changed");
  });
  it("retains the unread indicator after the final file is removed", () => {
    const removed = { ...trust(), surfaces: [{ ...trust().surfaces[0]!, count: 0, seen: false }] };
    expect(trustDotState(removed)).toBe("unseen");
    render(() => <TrustPopoverBody trust={removed} />);
    expect(screen.getByTestId("trust-popover-row-agents_md")).toBeTruthy();
  });
});
