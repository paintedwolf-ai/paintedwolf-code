import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it } from "vitest";
import { requestHostFolder } from "../platform/files/host-folder-dialog.ts";
import { HostFolderDialog } from "./HostFolderDialog.tsx";

afterEach(cleanup);

describe("host folder entry", () => {
  it("requires a path and preserves spaces and Unicode inside it", async () => {
    render(() => <HostFolderDialog />);
    const pending = requestHostFolder();
    const dialog = await screen.findByRole("dialog", { name: "Choose a folder" });
    const submit = screen.getByRole("button", { name: "Choose folder" });
    expect(submit.hasAttribute("disabled")).toBe(true);
    fireEvent.input(screen.getByLabelText("Folder path"), { target: { value: "  /host/café notes  " } });
    expect(submit.hasAttribute("disabled")).toBe(false);
    fireEvent.submit(dialog);
    await expect(pending).resolves.toBe("/host/café notes");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it.each(["button", "escape", "backdrop"])("cancels through %s without returning a path", async (action) => {
    render(() => <HostFolderDialog />);
    const pending = requestHostFolder();
    const dialog = await screen.findByRole("dialog", { name: "Choose a folder" });
    if (action === "button") fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    else if (action === "escape") fireEvent.keyDown(dialog, { key: "Escape" });
    else {
      const backdrop = dialog.parentElement;
      if (!backdrop) throw new Error("Folder dialog has no backdrop");
      fireEvent.click(backdrop);
    }
    await expect(pending).resolves.toBeNull();
  });

  it("settles each queued request exactly once with its own entered path", async () => {
    render(() => <HostFolderDialog />);
    const first = requestHostFolder();
    const second = requestHostFolder();
    const third = requestHostFolder();
    const dialog = await screen.findByRole("dialog");
    fireEvent.input(screen.getByLabelText("Folder path"), { target: { value: "/first" } });
    fireEvent.submit(dialog);
    await expect(first).resolves.toBe("/first");
    expect((screen.getByLabelText("Folder path") as HTMLInputElement).value).toBe("");
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await expect(second).resolves.toBeNull();
    fireEvent.input(screen.getByLabelText("Folder path"), { target: { value: "/third" } });
    fireEvent.submit(dialog);
    await expect(third).resolves.toBe("/third");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("settles all pending requests on disposal and refuses requests without a presenter", async () => {
    const { unmount } = render(() => <HostFolderDialog />);
    const pending = Array.from({ length: 12 }, () => requestHostFolder());
    unmount();
    await expect(Promise.all(pending)).resolves.toEqual(Array.from({ length: 12 }, () => null));
    await expect(requestHostFolder()).rejects.toThrow("folder picker is not ready");
  });
});
