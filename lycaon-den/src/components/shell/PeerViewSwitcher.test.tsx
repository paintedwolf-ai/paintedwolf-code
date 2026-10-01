import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import {
  PeerViewSwitcher,
  type PeerViewSwitcherHandle,
} from "./PeerViewSwitcher.tsx";
import type { EditorWindow } from "../../platform/windows/editor-windows.ts";

const VIEWS: EditorWindow[] = [
  {
    clientId: "window:session:s1:1",
    label: "Window 1",
    slot: 1,
    nativeLabel: "session:s1:1",
    title: "First",
  },
  {
    clientId: "window:session:s1:2",
    label: "Window 2",
    slot: 2,
    nativeLabel: "session:s1:2",
    title: "Second",
  },
];

const MAIN: EditorWindow = {
  clientId: "window:main",
  label: "Main window",
  slot: 0,
  nativeLabel: "main",
  title: "First",
};

describe("PeerViewSwitcher", () => {
  let handle: PeerViewSwitcherHandle | undefined;

  beforeEach(() => {
    handle = undefined;
  });

  afterEach(() => {
    handle?.cancel();
  });

  it("focuses the selected peer and closes on blur", () => {
    const onFocus = vi.fn();
    const onClose = vi.fn();
    const onOpenChange = vi.fn();
    render(() => (
      <PeerViewSwitcher
        ref={(h) => {
          handle = h;
        }}
        views={VIEWS}
        onFocus={onFocus}
        onClose={onClose}
        onOpenChange={onOpenChange}
      />
    ));
    handle?.open();
    expect(onOpenChange).toHaveBeenCalledWith(true);
    const dialog = screen.getByTestId("peer-view-switcher");
    expect(dialog).toBeTruthy();
    expect(screen.getAllByTestId("peer-view-switcher-row")).toHaveLength(2);

    fireEvent.keyDown(dialog, { key: "ArrowDown" });
    fireEvent.keyDown(dialog, { key: "Enter" });
    expect(onFocus).toHaveBeenCalledWith(VIEWS[1]);
    expect(onOpenChange).toHaveBeenCalledWith(false);

    handle?.open();
    fireEvent.blur(window);
    expect(screen.queryByTestId("peer-view-switcher")).toBeNull();
  });

  it("closes a peer from the row action without focusing it", () => {
    const onFocus = vi.fn();
    const onClose = vi.fn();
    render(() => (
      <PeerViewSwitcher
        ref={(h) => {
          handle = h;
        }}
        views={VIEWS}
        onFocus={onFocus}
        onClose={onClose}
      />
    ));
    handle?.open();
    fireEvent.click(screen.getAllByTestId("peer-view-switcher-close")[0]!);
    expect(onClose).toHaveBeenCalledWith(VIEWS[0]);
    expect(onFocus).not.toHaveBeenCalled();
  });

  it("focuses the main window but never closes it", () => {
    const onFocus = vi.fn();
    const onClose = vi.fn();
    render(() => (
      <PeerViewSwitcher
        ref={(h) => {
          handle = h;
        }}
        views={[MAIN, VIEWS[1]!]}
        onFocus={onFocus}
        onClose={onClose}
      />
    ));
    handle?.open();
    expect(screen.getAllByTestId("peer-view-switcher-row")[0]!.textContent).toContain("Main window");
    expect(screen.getAllByTestId("peer-view-switcher-close")).toHaveLength(1);
    const dialog = screen.getByTestId("peer-view-switcher");
    fireEvent.keyDown(dialog, { key: "Delete" });
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.keyDown(dialog, { key: "Enter" });
    expect(onFocus).toHaveBeenCalledWith(MAIN);
  });
});
