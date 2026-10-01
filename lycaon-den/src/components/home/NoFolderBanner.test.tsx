import { describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import { NoFolderBanner } from "./NoFolderBanner.tsx";

describe("NoFolderBanner", () => {
  it("renders locked copy and fires Add folder / dismiss", () => {
    const onAddFolder = vi.fn();
    const onDismiss = vi.fn();
    const { getByTestId, getByText } = render(() => (
      <NoFolderBanner onAddFolder={onAddFolder} onDismiss={onDismiss} />
    ));

    expect(getByTestId("nofolder-banner")).toBeTruthy();
    expect(getByText("No folder attached")).toBeTruthy();
    expect(
      getByText("Add a folder so the assistant can read and edit your code."),
    ).toBeTruthy();

    fireEvent.click(getByTestId("nofolder-add"));
    fireEvent.click(getByTestId("nofolder-dismiss"));
    expect(onAddFolder).toHaveBeenCalledTimes(1);
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });
});
