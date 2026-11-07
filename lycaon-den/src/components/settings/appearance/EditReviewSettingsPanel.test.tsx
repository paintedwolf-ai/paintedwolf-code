import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { createSettingsStore } from "../../../store/settings-store.ts";
import { assertNamedControl } from "../../../test/controls.ts";
import { createAppStore } from "../../../store/app-state.ts";
import { mockProjectsStore } from "../../../test/projects-fixture.ts";
import { EditReviewSettingsPanel } from "./EditReviewSettingsPanel.tsx";

describe("edit review drafts", () => {
  it("preserves the focused input and pending edit through a host refresh", () => {
    const store = createSettingsStore();
    const review = { scope: "global" as const, review_paths: [{ path: "src/**", tool: "" }], merged_from: [] };
    store.actions.setReview(review);
    const view = render(() => <EditReviewSettingsPanel client={{ updateReviewSettings: vi.fn() } as never}
      settingsStore={store} appStore={createAppStore()} projects={mockProjectsStore()} />);
    const input = assertNamedControl(screen.getByTestId("edit-review-path-0"), "textbox") as HTMLInputElement;
    input.focus();
    fireEvent.input(input, { target: { value: "src/components/**" } });
    expect(screen.getByTestId("edit-review-path-0")).toBe(input);
    store.actions.setReview({ ...review, review_paths: [{ path: "src/**", tool: "" }] });
    expect(document.activeElement).toBe(input);
    expect(input.value).toBe("src/components/**");
    view.unmount();
  });
});
