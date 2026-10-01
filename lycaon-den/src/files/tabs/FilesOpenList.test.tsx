import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { stockBindingId } from "../../contributions/stock-frame-test.ts";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { FilesOpenList } from "./FilesOpenList.tsx";
import { applyFilesBufferLoad, applyFilesBufferLoadError, applyFilesBufferUnsupportedEncoding, markFilesBufferLoading, openFilesBuffer, resetProjectFilesForTests, setFilesBufferPinned } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import type { FileBufferKey } from "../components/project-files-model.ts";
import {
  resetShortcutPrefsForTests,
  saveShortcutOverride,
} from "../../settings/system/shortcut-prefs.ts";
import { setShortcutPlatformForTests } from "../../shortcuts/platform.ts";
import {
  resetDispatcherForTests,
  setFilesStageActive,
} from "../../shortcuts/dispatcher.ts";

const PROJECT = "open-list-p";

function load(path: string, intent: "transient" | "permanent" = "permanent") {
  const key = openFilesBuffer(PROJECT, {
    intent,
    rootId: "r1",
    rootLabel: "repo",
    path,
  });
  applyFilesBufferLoad(PROJECT, key, {
    file_id: "",
    version_id: "",
    workspace_id: "workspace-1",
    workspace_kind: "project",
    path,
    content: "x",
    over_limit: false,
    writable: true,
    binary: false,
    size_bytes: 1,
    sha256: "s",
  });
  return key;
}

describe("FilesOpenList", () => {
  it("identifies and filters special views without presenting a synthetic file path", () => {
    const key = load("placeholder");
    const base = projectFilesState(PROJECT).byKey[key]!;
    const rows = (["walk", "chat", "trust"] as const).map((kind) => ({
      key: kind,
      buffer: { ...base, key: kind, kind, name: `${kind} example`, path: "", preview: true,
        chatContent: kind === "chat" ? { kind: "tool" as const, sessionId: "session", messageId: "message", toolCallId: "call", pane: "details", title: "Tool call", content: { kind: "inline" as const, text: "" } } : undefined },
    }));
    render(() => <FilesOpenList open rows={rows} activeKey="chat" anchorEl={document.body}
      onClose={vi.fn()} onActivate={vi.fn()} onCloseBuffer={vi.fn()} onRowContextMenu={vi.fn()} />);
    for (const [index, label] of ["Group summary", "Tool call details", "Trust changes"].entries()) {
      const row = screen.getAllByTestId("files-open-list-row")[index]!;
      const activate = row.querySelector(".den-files-open-list__activate")!;
      expect(activate.getAttribute("aria-label")).toContain(`${label}, Preview`);
      expect(activate.hasAttribute("data-tip")).toBe(false);
      expect(row.querySelector("[data-tip]")).toBeNull();
      expect(row.querySelector(".den-files-open-list__dir")).toBeNull();
      expect(row.querySelector(".den-files-open-list__kind")).toBeNull();
    }
    fireEvent.input(screen.getByTestId("files-open-list-filter"), { target: { value: "group summary" } });
    expect(screen.getAllByTestId("files-open-list-row")).toHaveLength(1);
    expect(screen.getByTestId("files-open-list-row").getAttribute("data-key")).toBe("walk");
  });

  it("includes inactive historical tabs when filtering by previous version", () => {
    const key = load("src/history.ts");
    const currentKey = load("src/current.ts");
    render(() => <FilesOpenList open rows={[
      { key, buffer: projectFilesState(PROJECT).byKey[key]!, previousVersion: true },
      { key: currentKey, buffer: projectFilesState(PROJECT).byKey[currentKey]! },
    ]} activeKey={currentKey} anchorEl={document.body}
      onClose={vi.fn()} onActivate={vi.fn()} onCloseBuffer={vi.fn()} onRowContextMenu={vi.fn()} />);
    fireEvent.input(screen.getByTestId("files-open-list-filter"), { target: { value: "previous version" } });
    expect(screen.getAllByTestId("files-open-list-row")).toHaveLength(1);
    const row = screen.getByTestId("files-open-list-row");
    expect(row.getAttribute("data-key")).toBe(key);
    expect(row.querySelector("[data-tip]")).toBeNull();
    expect(row.querySelector(".den-files-open-list__activate")?.getAttribute("aria-label")).toContain("Previous version");
    expect(row.querySelector(".den-files-open-list__activate")?.getAttribute("aria-current")).toBeNull();
  });

  beforeEach(() => {
    localStorage.clear();
    resetProjectFilesForTests();
    resetShortcutPrefsForTests();
    setShortcutPlatformForTests("macos");
    setFilesStageActive(true);
  });

  afterEach(() => {
    localStorage.clear();
    resetShortcutPrefsForTests();
    resetDispatcherForTests();
    setShortcutPlatformForTests(null);
  });

  it("labels file limitations accurately and hides unverified kinds", () => {
    const key = load("Cargo.toml");
    const buffer = projectFilesState(PROJECT).byKey[key]!;
    render(() => <FilesOpenList open rows={[{ key, buffer }]} activeKey={key} anchorEl={document.body}
      onClose={vi.fn()} onActivate={vi.fn()} onCloseBuffer={vi.fn()} onRowContextMenu={vi.fn()} />);
    const label = () => screen.getByTestId("files-open-list-row").querySelector(".den-files-open-list__kind")?.textContent;
    expect(label()).toBeUndefined();
    const source = {
      file_id: "", version_id: "", workspace_id: "workspace-1", workspace_kind: "project" as const,
      path: "Cargo.toml", content: "", binary: false, over_limit: true, writable: true, size_bytes: 5_000_000,
    };
    applyFilesBufferLoad(PROJECT, key, source);
    expect(label()).toBe("Too large");
    applyFilesBufferUnsupportedEncoding(PROJECT, key, "unknown");
    expect(label()).toBe("Encoding");
    applyFilesBufferLoad(PROJECT, key, { ...source, binary: true, over_limit: false });
    expect(label()).toBe("Binary");
    applyFilesBufferLoad(PROJECT, key, { ...source, binary: true, over_limit: false, mime: "image/png" });
    expect(label()).toBe("Image");
    markFilesBufferLoading(PROJECT, key);
    expect(label()).toBeUndefined();
    applyFilesBufferLoadError(PROJECT, key, "Permission denied");
    expect(label()).toBeUndefined();
  });

  it("lists open buffers in strip order with pin separator and filter", async () => {
    const a = load("src/a.ts");
    const b = load("src/b.ts");
    setFilesBufferPinned(PROJECT, b, true);
    load("src/c.ts");

    const onActivate = vi.fn();
    const onCloseBuffer = vi.fn();
    const onRowContextMenu = vi.fn();
    const onClose = vi.fn();
    const rows = () =>
      projectFilesState(PROJECT).order
        .map((key) => {
          const buffer = projectFilesState(PROJECT).byKey[key];
          return buffer ? { key, buffer } : null;
        })
        .filter((r): r is { key: FileBufferKey; buffer: NonNullable<typeof r>["buffer"] } =>
          r != null,
        );

    // The caller supplies pinned rows first.
    const ordered = [
      ...rows().filter((r) => r.buffer.pinned),
      ...rows().filter((r) => !r.buffer.pinned),
    ];

    const { unmount } = render(() => (
      <FilesOpenList
        open
        rows={ordered}
        activeKey={a}
        anchorEl={document.body}
        onClose={onClose}
        onActivate={onActivate}
        onCloseBuffer={onCloseBuffer}
        onRowContextMenu={onRowContextMenu}
      />
    ));

    const listRows = screen.getAllByTestId("files-open-list-row");
    expect(
      screen.getByTestId("files-open-list").dataset.denAnchoredSurface,
    ).toBeTruthy();
    const list = screen.getByRole("list", { name: "Open files" });
    expect(list.classList.contains("den-files-open-list__rows-body")).toBe(true);
    expect(
      list.closest(".den-files-open-list__rows")?.hasAttribute("data-den-scrollport"),
    ).toBe(true);
    expect(listRows[0]?.getAttribute("data-key")).toBe(b);
    expect(screen.getByTestId("files-open-list-pin-sep")).toBeTruthy();

    fireEvent.input(screen.getByTestId("files-open-list-filter"), {
      target: { value: "zzz" },
    });
    expect(screen.getByTestId("files-open-list-empty").textContent).toContain(
      'No open files match "zzz"',
    );

    fireEvent.input(screen.getByTestId("files-open-list-filter"), {
      target: { value: "a.ts" },
    });
    const filtered = screen.getAllByTestId("files-open-list-row");
    expect(filtered).toHaveLength(1);
    fireEvent.click(
      filtered[0]!.querySelector(".den-files-open-list__activate")!,
    );
    expect(onActivate).toHaveBeenCalledWith(a);
    expect(onClose).toHaveBeenCalled();

    unmount();
  });

  it("keyboard matrix: arrows, Enter, live close-tab binding, Escape", async () => {
    load("a.ts");
    const b = load("b.ts");
    const onActivate = vi.fn();
    const onCloseBuffer = vi.fn();
    const onClose = vi.fn();
    const anchor = document.createElement("button");
    document.body.appendChild(anchor);
    const focusSpy = vi.spyOn(anchor, "focus");

    const rows = projectFilesState(PROJECT).order.map((key) => ({
      key,
      buffer: projectFilesState(PROJECT).byKey[key]!,
    }));

    const { unmount } = render(() => (
      <FilesOpenList
        open
        rows={rows}
        activeKey={b}
        anchorEl={anchor}
        onClose={onClose}
        onActivate={onActivate}
        onCloseBuffer={onCloseBuffer}
        onRowContextMenu={() => undefined}
      />
    ));

    const panel = screen.getByTestId("files-open-list");
    fireEvent.keyDown(panel, { key: "ArrowDown" });
    fireEvent.keyDown(panel, { key: "Enter" });
    expect(onActivate).toHaveBeenCalled();
    expect(onClose).toHaveBeenCalled();

    onClose.mockClear();
    fireEvent.keyDown(panel, { key: "w", code: "KeyW", metaKey: true });
    expect(onCloseBuffer).toHaveBeenCalled();

    fireEvent.keyDown(panel, { key: "Escape" });
    expect(onClose).toHaveBeenCalled();
    expect(focusSpy).toHaveBeenCalled();

    unmount();
    anchor.remove();
  });

  it("uses live confirm and dismiss bindings instead of fixed widget keys", async () => {
    const key = load("a.ts");
    expect(
      await saveShortcutOverride(stockBindingId("list-confirm"), "Mod+O"),
    ).toEqual({ ok: true });
    expect(
      await saveShortcutOverride(
        stockBindingId("overlay-dismiss"),
        "Mod+Alt+E",
      ),
    ).toEqual({ ok: true });
    const onActivate = vi.fn();
    const onClose = vi.fn();
    const rows = [
      { key, buffer: projectFilesState(PROJECT).byKey[key]! },
    ];

    render(() => (
      <FilesOpenList
        open
        rows={rows}
        activeKey={key}
        anchorEl={null}
        onClose={onClose}
        onActivate={onActivate}
        onCloseBuffer={vi.fn()}
        onRowContextMenu={() => undefined}
      />
    ));

    const panel = screen.getByTestId("files-open-list");
    fireEvent.keyDown(panel, { key: "Enter", code: "Enter" });
    expect(onActivate).not.toHaveBeenCalled();
    fireEvent.keyDown(panel, { key: "o", code: "KeyO", metaKey: true });
    expect(onActivate).toHaveBeenCalledWith(key);

    onClose.mockClear();
    const escape = new KeyboardEvent("keydown", {
      key: "Escape",
      code: "Escape",
      bubbles: true,
      cancelable: true,
    });
    fireEvent(panel, escape);
    expect(onClose).not.toHaveBeenCalled();
    expect(escape.defaultPrevented).toBe(false);
    fireEvent.keyDown(panel, {
      key: "e",
      code: "KeyE",
      metaKey: true,
      altKey: true,
    });
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("row close and context menu never invent non-open files", () => {
    const key = load("only.ts");
    const onCloseBuffer = vi.fn();
    const onRowContextMenu = vi.fn();
    const rows = [
      {
        key,
        buffer: projectFilesState(PROJECT).byKey[key]!,
      },
    ];
    const { unmount } = render(() => (
      <FilesOpenList
        open
        rows={rows}
        activeKey={key}
        anchorEl={null}
        onClose={() => undefined}
        onActivate={() => undefined}
        onCloseBuffer={onCloseBuffer}
        onRowContextMenu={onRowContextMenu}
      />
    ));
    fireEvent.click(screen.getByTestId("files-open-list-close"));
    expect(onCloseBuffer).toHaveBeenCalledWith(key);
    fireEvent.contextMenu(screen.getByTestId("files-open-list-row"));
    expect(onRowContextMenu).toHaveBeenCalledWith(
      key,
      expect.objectContaining({ x: expect.any(Number), y: expect.any(Number) }),
    );
    expect(screen.getAllByTestId("files-open-list-row")).toHaveLength(1);
    unmount();
  });
});
