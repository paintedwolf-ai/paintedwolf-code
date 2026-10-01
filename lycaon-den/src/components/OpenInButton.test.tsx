import { createSignal } from "solid-js";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { OpenInButton } from "./OpenInButton.tsx";
import { ContextMenu } from "./ContextMenu.tsx";
import { openInMenuItems } from "./open-in-menu-items.ts";
const editor = vi.hoisted(() => vi.fn(async () => ({ status: "opened" })));
vi.mock("../platform/runtime.ts", () => ({ isTauriRuntime: () => true, tauriPlatform: () => "macos" }));
vi.mock("../platform/navigation/external-source.ts", () => ({ openInExternalEditor: editor }));

describe("open destinations interaction", () => {
  it("keeps the captured path when the surrounding selection changes", async () => {
    const [path, setPath] = createSignal("/repo/first.ts");
    render(() => <OpenInButton target={{ absolutePath: path(), projectRoots: ["/repo"], entryKind: "file" }} />);
    const button = screen.getByTestId("open-in-button");
    fireEvent.click(button);
    setPath("/repo/second.ts");
    fireEvent.click(screen.getByTestId("open-in-editor"));
    await waitFor(() => expect(editor).toHaveBeenCalledWith(expect.objectContaining({ absolutePath: "/repo/first.ts" })));
    expect(screen.queryByTestId("context-menu")).toBeNull();
  });
  it("enters and leaves the shared submenu with arrow keys", async () => {
    render(() => <ContextMenu anchor={{ x: 20, y: 20 }} onDismiss={vi.fn()}
      items={openInMenuItems({ absolutePath: "/repo/index.html", projectRoots: ["/repo"], entryKind: "file" })} />);
    const parent = screen.getByTestId("open-in-menu");
    fireEvent.keyDown(parent, { key: "ArrowRight" });
    await waitFor(() => expect(document.activeElement).toBe(screen.getByTestId("open-in-file-manager")));
    expect(screen.getByTestId("open-in-browser")).toBeTruthy();
    fireEvent.keyDown(document.activeElement!, { key: "ArrowLeft" });
    await waitFor(() => expect(document.activeElement).toBe(parent));
  });
});
