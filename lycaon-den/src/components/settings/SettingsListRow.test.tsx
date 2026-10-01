import { describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import { SettingsListRow } from "./SettingsListRow.tsx";

describe("SettingsListRow", () => {
  it("calls onSelect when the row is clicked", () => {
    const onSelect = vi.fn();
    const { getByTestId } = render(() => (
      <SettingsListRow
        testId="row-a"
        primary="Alpha"
        onSelect={onSelect}
      />
    ));
    fireEvent.click(getByTestId("row-a"));
    expect(onSelect).toHaveBeenCalledTimes(1);
  });

  it("applies selected class when selected", () => {
    const { getByTestId } = render(() => (
      <div>
        <SettingsListRow
          testId="row-b"
          primary="Beta"
          selected
          onSelect={() => {}}
        />
      </div>
    ));
    const hit = getByTestId("row-b");
    expect(hit.getAttribute("aria-pressed")).toBe("true");
    expect(hit.closest(".den-settings-list-row")?.className).toContain(
      "den-settings-list-row--selected",
    );
  });

  it("offers no control when the row is informational", () => {
    const { getByTestId } = render(() => (
      <SettingsListRow testId="row-d" primary="Delta" variant="rejected" />
    ));
    const hit = getByTestId("row-d");
    expect(hit.tagName).toBe("DIV");
    expect(hit.getAttribute("aria-pressed")).toBeNull();
  });

  it("does not call onSelect when leading control is clicked", () => {
    const onSelect = vi.fn();
    const { getByTestId } = render(() => (
      <SettingsListRow
        testId="row-c"
        primary="Gamma"
        leading={
          <button type="button" data-testid="row-c-leading">
            Toggle
          </button>
        }
        onSelect={onSelect}
      />
    ));
    fireEvent.click(getByTestId("row-c-leading"));
    expect(onSelect).not.toHaveBeenCalled();
  });
});
