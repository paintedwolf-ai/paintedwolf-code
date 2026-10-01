import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { focusRegion } from "../../shortcuts/focus-region.ts";
import { createAppStore } from "../../store/app-state.ts";
import { SettingsStagePanel } from "./SettingsStagePanel.tsx";

describe("SettingsStagePanel", () => {
  it("renders the routed back chip and wires onBack", () => {
    const appStore = createAppStore();
    const onBack = vi.fn();
    render(() => (
      <SettingsStagePanel
        appStore={appStore}
        back={{ label: "Back to chat", onBack, testId: "settings-back" }}
      >
        <div>panel</div>
      </SettingsStagePanel>
    ));
    const back = screen.getByTestId("settings-back");
    expect(back.textContent).toContain("Back to chat");
    fireEvent.click(back);
    expect(onBack).toHaveBeenCalledOnce();
  });

  it("renders no back chip for a deliberate visit", () => {
    const appStore = createAppStore();
    const { container } = render(() => (
      <SettingsStagePanel appStore={appStore}>
        <div>panel</div>
      </SettingsStagePanel>
    ));
    expect(container.querySelector(".den-stage-back")).toBeNull();
  });

  it("forwards region entry inward and leaves pointer focus on the panel", () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const { container } = render(() => (
      <SettingsStagePanel
        appStore={appStore}
        offline={<button type="button">first</button>}
      />
    ));
    const panel = container.querySelector<HTMLElement>(".den-settings-panel")!;
    const first = screen.getByText("first");

    // WebKit never mouse-focuses buttons; the click focuses the tabindex=-1 panel.
    panel.focus();
    expect(document.activeElement).toBe(panel);

    panel.blur();
    expect(focusRegion("settings")).toBe(true);
    expect(document.activeElement).toBe(first);
  });
});
