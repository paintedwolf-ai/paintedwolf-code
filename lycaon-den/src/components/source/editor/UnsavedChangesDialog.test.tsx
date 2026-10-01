import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { UnsavedChangesDialog } from "./UnsavedChangesDialog.tsx";

describe("UnsavedChangesDialog bulk-close", () => {
  it("lists capped names and offers Save all / Discard all / Cancel", () => {
    const onCancel = vi.fn();
    const onDiscard = vi.fn();
    const onSave = vi.fn();
    const names = Array.from({ length: 10 }, (_, i) => `file-${i}.ts`);
    const { unmount } = render(() => (
      <UnsavedChangesDialog
        intent="bulk-close"
        canSave
        fileNames={names}
        onCancel={onCancel}
        onDiscard={onDiscard}
        onSave={onSave}
      />
    ));

    expect(screen.getByTestId("editor-unsaved-bulk-body")).toBeTruthy();
    expect(screen.getByTestId("editor-unsaved-and-more").textContent).toContain(
      "and 2 more",
    );
    expect(screen.getByTestId("editor-unsaved-discard").textContent).toContain(
      "Discard all",
    );
    expect(screen.getByTestId("editor-unsaved-save").textContent).toContain(
      "Save all",
    );

    fireEvent.click(screen.getByTestId("editor-unsaved-cancel"));
    expect(onCancel).toHaveBeenCalled();
    fireEvent.click(screen.getByTestId("editor-unsaved-discard"));
    expect(onDiscard).toHaveBeenCalled();
    fireEvent.click(screen.getByTestId("editor-unsaved-save"));
    expect(onSave).toHaveBeenCalled();
    unmount();
  });
});

describe("UnsavedChangesDialog with a held AI edit", () => {
  it.each(["discard", "reload"] as const)(
    "says the AI's edit survives %s",
    (intent) => {
      const { unmount } = render(() => (
        <UnsavedChangesDialog
          intent={intent}
          heldAgentEdit
          onCancel={vi.fn()}
          onDiscard={vi.fn()}
        />
      ));

      expect(
        screen.getByTestId("editor-unsaved-held-agent-edit").textContent,
      ).toContain("kept in version history");
      unmount();
    },
  );

  it("says nothing about version history when the draft is only the person's", () => {
    const { unmount } = render(() => (
      <UnsavedChangesDialog
        intent="discard"
        onCancel={vi.fn()}
        onDiscard={vi.fn()}
      />
    ));

    expect(screen.queryByTestId("editor-unsaved-held-agent-edit")).toBeNull();
    unmount();
  });
});
