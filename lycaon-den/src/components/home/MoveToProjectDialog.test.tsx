import { describe, expect, it, vi } from "vitest";
import { render, fireEvent, waitFor } from "@solidjs/testing-library";
import { MoveToProjectDialog } from "./MoveToProjectDialog.tsx";

describe("MoveToProjectDialog", () => {
  it("submits the chosen folder and git toggle", async () => {
    const onSubmit = vi.fn();
    const onPickFolder = vi.fn().mockResolvedValue("/code/budget");
    const { getByTestId } = render(() => (
      <MoveToProjectDialog
        open
        onPickFolder={onPickFolder}
        onSubmit={onSubmit}
        onClose={vi.fn()}
      />
    ));

    // Submit is disabled until a folder is chosen.
    expect((getByTestId("move-project-submit") as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(getByTestId("move-project-choose"));
    await waitFor(() =>
      expect(getByTestId("move-project-folder").textContent).toContain("/code/budget"),
    );

    fireEvent.click(getByTestId("move-project-submit"));
    expect(onSubmit).toHaveBeenCalledWith({ rootPath: "/code/budget", initGit: true });
  });

  it("renders a server error", () => {
    const { getByTestId } = render(() => (
      <MoveToProjectDialog
        open
        error="the folder must be empty"
        onPickFolder={vi.fn()}
        onSubmit={vi.fn()}
        onClose={vi.fn()}
      />
    ));
    expect(getByTestId("move-project-error").textContent).toContain("must be empty");
  });

  it("controls Escape while the modal is open", () => {
    const onClose = vi.fn();
    const { getByTestId } = render(() => (
      <MoveToProjectDialog
        open
        onPickFolder={vi.fn()}
        onSubmit={vi.fn()}
        onClose={onClose}
      />
    ));
    fireEvent.keyDown(getByTestId("move-project-dialog"), { key: "Escape" });
    expect(onClose).toHaveBeenCalledOnce();
  });
});
