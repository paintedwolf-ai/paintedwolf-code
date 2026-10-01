import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MoveToTrashDialog, type TrashConfirmState } from "./MoveToTrashDialog.tsx";

vi.mock("../../platform/connection/host-identity.ts", () => ({ hostSharesDevice: () => true }));
vi.mock("../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/runtime.ts")>()),
  tauriPlatform: () => "macos",
  usesCustomWindowChrome: () => true,
  tauriDragRegionProps: () => ({ "data-tauri-drag-region": "" }),
}));

const selection: TrashConfirmState = {
  operationId: "operation-1", rootId: "root-1", path: "notes.md", name: "notes.md",
  isDir: false, body: "Move notes.md to trash. You can undo this change.",
};

afterEach(cleanup);

describe("Move to trash", () => {
  it("confirms the selected file with an accessible description", () => {
    const confirm = vi.fn();
    render(() => <MoveToTrashDialog state={selection} onCancel={() => {}} onConfirm={confirm} />);
    expect(screen.getByRole("alertdialog").getAttribute("aria-describedby")).toBe("files-trash-body");
    fireEvent.click(screen.getByRole("button", { name: "Move to trash" }));
    expect(confirm).toHaveBeenCalledOnce();
  });

  it("offers retry and the host recovery guidance after an operational failure", () => {
    const confirm = vi.fn();
    render(() => <MoveToTrashDialog state={{ ...selection, error: "Trash unavailable", retryable: true, suggestedAction: "Check disk space." }} onCancel={() => {}} onConfirm={confirm} />);
    expect(screen.getByRole("heading").textContent).toContain("Couldn’t move “notes.md”");
    expect(screen.getByText("Check disk space.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(confirm).toHaveBeenCalledOnce();
  });

  it("does not offer retry when the host requires a different action", () => {
    render(() => <MoveToTrashDialog state={{ ...selection, error: "Protected project metadata", retryable: false }} onCancel={() => {}} onConfirm={() => {}} />);
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
    expect(screen.getByRole("button", { name: "Close" })).toBeTruthy();
  });

  it("prevents duplicate submissions while a move is running", () => {
    const confirm = vi.fn();
    const cancel = vi.fn();
    render(() => <MoveToTrashDialog state={selection} busy onCancel={cancel} onConfirm={confirm} />);
    expect((screen.getByRole("button", { name: "Moving…" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Continue working" }) as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Continue working" }));
    expect(cancel).toHaveBeenCalledOnce();
  });

  it.each([false, true])("keeps window chrome available with busy=%s without dismissing the modal", async (busy) => {
    const appRoot = document.createElement("div");
    appRoot.id = "root";
    document.body.append(appRoot);
    const cancel = vi.fn();
    const view = render(() => <MoveToTrashDialog state={selection} busy={busy} onCancel={cancel} onConfirm={() => {}} />);
    await Promise.resolve();

    const backdrop = document.querySelector(".den-dialog-backdrop")!;
    const dragSurface = backdrop.querySelector<HTMLElement>("[data-tauri-drag-region]")!;
    expect(dragSurface).not.toBeNull();
    expect(appRoot.hasAttribute("inert")).toBe(true);
    expect(dragSurface.closest("[inert]")).toBeNull();
    expect(dragSurface.tabIndex).toBe(-1);
    expect(screen.getByRole("alertdialog").contains(dragSurface)).toBe(false);

    fireEvent.click(dragSurface);
    expect(cancel).not.toHaveBeenCalled();
    fireEvent.click(backdrop);
    expect(cancel).toHaveBeenCalledTimes(1);

    view.unmount();
    expect(appRoot.hasAttribute("inert")).toBe(false);
    expect(document.querySelector(".den-dialog-backdrop__chrome-drag")).toBeNull();
    appRoot.remove();
  });
});
