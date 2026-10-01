import { render, fireEvent, screen, cleanup, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { DocumentWindows } from "./DocumentWindows.tsx";
import type { DocumentReplica } from "../documents/document-replica.ts";
import type { ItemWindowView } from "../../platform/windows/item-windows.ts";
import type { EditorWindow } from "../../platform/windows/editor-windows.ts";

const host = vi.hoisted(() => ({ focus: vi.fn(async () => true), close: vi.fn(async () => true), views: (): readonly ItemWindowView[] => [] }));
vi.mock("../../platform/connection/client-identity.ts", () => ({ clientIdentity: () => "window:main" }));
vi.mock("../../platform/windows/item-windows.ts", () => ({ itemWindowViews: () => host.views(), focusItemWindow: host.focus, closeItemWindow: host.close }));
afterEach(() => { registerNoticePublisher(null); cleanup(); vi.clearAllMocks(); host.views = () => []; });
const peer = (slot: number): EditorWindow => ({ clientId: `window:file:${slot}`, label: `Window ${slot}`, slot, nativeLabel: `file:${slot}`, title: "shared.ts" });

describe("document windows", () => {
  it("lists this window and stable peer identities, focuses exactly the selected window", async () => {
    render(() => <DocumentWindows projectId="p1" peerViews={[peer(2), peer(7)]} />);
    fireEvent.click(screen.getByRole("button", { name: "Open in 3 windows" }));
    expect(screen.getByRole("menu", { name: "Windows with this file open" })).toBeTruthy();
    expect(screen.getByRole<HTMLButtonElement>("menuitem", { name: "Main window, this window" }).disabled).toBe(true);
    fireEvent.click(screen.getByRole("menuitem", { name: "Focus window 7" }));
    await waitFor(() => expect(host.focus).toHaveBeenCalledWith("file:7"));
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
  });

  it("excludes departed native participants even when a delayed snapshot includes them", () => {
    const [views, setViews] = createSignal<ItemWindowView[]>([2, 9].map(slot => ({
      label: `file:${slot}`, title: "shared.ts", viewNumber: slot, kind: "file", projectId: "p",
    })));
    host.views = views;
    let notify = () => {};
    const replica = {
      clientId: "window:main",
      accepted: { participants: [2, 9].map(slot => ({ client_id: `window:file:${slot}`, window_number: slot + 20 })) },
      subscribe: (listener: () => void) => { notify = listener; return () => {}; },
    } as unknown as DocumentReplica;
    render(() => <DocumentWindows projectId="p1" replica={replica} />);
    fireEvent.click(screen.getByRole("button", { name: "Open in 3 windows" }));
    setViews(views().filter(window => window.viewNumber === 9));
    notify();
    expect(screen.queryByRole("menuitem", { name: "Focus window 2" })).toBeNull();
    expect(screen.getByRole("menuitem", { name: "Focus window 9" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Open in 2 windows" })).toBeTruthy();
    setViews([]);
    notify();
    expect(screen.queryByTestId("document-windows")).toBeNull();
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("keeps close available and reports a failed focus to the project's notices", async () => {
    const notices = createNoticeStore();
    registerNoticePublisher(notices);
    host.focus.mockResolvedValueOnce(false);
    render(() => <DocumentWindows projectId="p1" peerViews={[peer(3)]} />);
    fireEvent.click(screen.getByRole("button", { name: "Open in 2 windows" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Focus window 3" }));
    await waitFor(() => expect(selectProjectNoticeGroups(notices.index()).find(group => group.projectId === "p1")?.notices[0]?.message).toContain("no longer available"));
    expect(screen.queryByRole("alert")).toBeNull();
    fireEvent.click(screen.getByRole("menuitem", { name: "Close window 3" }));
    await waitFor(() => expect(host.close).toHaveBeenCalledWith("file:3"));
  });

  it("removes stale rows and the indicator when the last peer leaves", async () => {
    const [peers, setPeers] = createSignal([peer(2), peer(9)]);
    render(() => <DocumentWindows projectId="p1" peerViews={peers()} />);
    fireEvent.click(screen.getByRole("button", { name: "Open in 3 windows" }));
    setPeers([peer(9)]);
    expect(screen.queryByRole("menuitem", { name: "Focus window 2" })).toBeNull();
    expect(screen.getByRole("menuitem", { name: "Focus window 9" })).toBeTruthy();
    setPeers([]);
    expect(screen.queryByTestId("document-windows")).toBeNull();
    expect(screen.queryByRole("menu")).toBeNull();
  });
});
