import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { SourcePathLink } from "./SourcePathLink.tsx";
import { SourceUrlLink } from "./SourceUrlLink.tsx";
import { createSignal } from "solid-js";
import { NoticeReporterProvider } from "../../notices/notice-reporter.tsx";

const openSourceLocation = vi.hoisted(() => vi.fn());
const confirmAndOpenExternalLink = vi.hoisted(() => vi.fn());
const browseProjectSource = vi.hoisted(() => vi.fn());

vi.mock("../../platform/navigation/open-source.ts", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../platform/navigation/open-source.ts")>(), openSourceLocation,
}));
vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: () => ({ browseProjectSource }) }));

vi.mock("../../platform/desktop/external-link.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../platform/desktop/external-link.ts")>();
  return {
    ...actual,
    confirmAndOpenExternalLink,
  };
});

describe("SourcePathLink", () => {
  beforeEach(() => {
    openSourceLocation.mockReset();
    openSourceLocation.mockResolvedValue({ status: "opened-in-app" });
    browseProjectSource.mockReset();
  });

  it("keeps the context menu bound to the source that opened it", () => {
    const [path, setPath] = createSignal("first.ts");
    render(() => <SourcePathLink projectId="p1" path={path()} line={12}
      rootRefs={[{ id: "root-1", path: "/repo" }]} />);
    fireEvent.contextMenu(screen.getByTestId("source-path-link"));
    setPath("second.ts");
    fireEvent.click(screen.getByTestId("path-menu-open"));
    expect(openSourceLocation).toHaveBeenCalledWith(expect.objectContaining({ path: "first.ts", rootId: "root-1", line: 12 }));
  });

  it("keeps a changing label, click destination and menu on the same attached root", () => {
    const [path, setPath] = createSignal("@other/first");
    render(() => <SourcePathLink projectId="p1" path={path()} label={path()} rootRefs={[
      { id: "primary", path: "/repo", is_primary: true },
      { id: "other", path: "/elsewhere", label: "other" },
    ]} />);
    const link = screen.getByTestId("source-path-link");
    setPath("@other/second");
    expect(link.textContent).toBe("@other/second");
    fireEvent.click(link);
    expect(openSourceLocation).toHaveBeenLastCalledWith(expect.objectContaining({ rootId: "other", path: "second" }));
    fireEvent.contextMenu(link);
    fireEvent.click(screen.getByTestId("path-menu-open"));
    expect(openSourceLocation).toHaveBeenLastCalledWith(expect.objectContaining({ rootId: "other", path: "second" }));
  });

  it("opens a root link as a folder with a root-bound destination", () => {
    render(() => <SourcePathLink projectId="p1" rootId="other" path="." rootRefs={[
      { id: "primary", path: "/repo", is_primary: true },
      { id: "other", path: "/elsewhere" },
    ]} />);
    fireEvent.click(screen.getByTestId("source-path-link"));
    expect(openSourceLocation).toHaveBeenCalledWith(expect.objectContaining({ path: ".", rootId: "other", entryKind: "folder" }));
  });

  it("stamps a path relative to its selected nested root", () => {
    render(() => <SourcePathLink projectId="p1" path="nested/file.ts" rootRefs={[
      { id: "primary", path: "/repo", is_primary: true },
      { id: "nested", path: "/repo/nested" },
    ]} />);
    const link = screen.getByTestId("source-path-link");
    expect(link.getAttribute("data-root-id")).toBe("nested");
    expect(link.getAttribute("data-den-source-path")).toBe("file.ts");
  });

  it("reports unresolvable transcript roots instead of trying a different app root", () => {
    const reportError = vi.fn();
    render(() => <NoticeReporterProvider reporter={{ reportError, publish: vi.fn() }}>
      <SourcePathLink projectId="p1" path="@missing/file.ts" rootRefs={[{ id: "r", path: "/repo" }]} />
    </NoticeReporterProvider>);
    fireEvent.click(screen.getByTestId("source-path-link"));
    expect(openSourceLocation).not.toHaveBeenCalled();
    expect(reportError).toHaveBeenCalledOnce();
  });

  it("reveals an untyped directory through its context menu using the host kind", async () => {
    browseProjectSource.mockResolvedValue({ entries: [{ name: "folder", is_dir: true }] });
    render(() => <SourcePathLink projectId="p1" path="folder" entryKind="unknown" rootRefs={[{ id: "r", path: "/repo" }]} />);
    fireEvent.contextMenu(screen.getByTestId("source-path-link"));
    fireEvent.click(await screen.findByTestId("path-menu-reveal-tree"));
    expect(openSourceLocation).toHaveBeenCalledWith(expect.objectContaining({ path: "folder", rootId: "r", action: "reveal", entryKind: "folder" }));
  });

  it("opens the menu's resolved directory after the displayed path changes", async () => {
    browseProjectSource.mockResolvedValue({ entries: [{ name: "folder", is_dir: true }] });
    const [path, setPath] = createSignal("folder");
    render(() => <SourcePathLink projectId="p1" path={path()} entryKind="unknown"
      rootRefs={[{ id: "r", path: "/repo" }]} />);
    fireEvent.contextMenu(screen.getByTestId("source-path-link"));
    const open = await screen.findByTestId("path-menu-open");
    setPath("another-folder");
    fireEvent.click(open);
    expect(openSourceLocation).toHaveBeenCalledWith(expect.objectContaining({
      path: "folder", rootId: "r", entryKind: "folder",
    }));
    expect(browseProjectSource).toHaveBeenCalledOnce();
  });

  it("opens path:line on click", async () => {
    render(() => (
      <SourcePathLink projectId="p1" path="pkg/x.go" line={4} />
    ));
    fireEvent.click(screen.getByTestId("source-path-link"));
    expect(openSourceLocation).toHaveBeenCalledWith({
      intent: "permanent",
      projectId: "p1",
      path: "pkg/x.go",
      line: 4,
    });
    expect(screen.getByTestId("source-path-link").hasAttribute("data-tip")).toBe(false);
  });

  it("only offers its label as a tooltip when a truncated label is clipped", () => {
    render(() => (
      <SourcePathLink projectId="p1" path="pkg/long-name.go" truncate />
    ));
    const link = screen.getByTestId("source-path-link");
    expect(link.getAttribute("data-tip")).toBe("pkg/long-name.go");
    expect(link.hasAttribute("data-tip-when-clipped")).toBe(true);
  });

  it("stamps provenance data attrs for selection walks", () => {
    render(() => (
      <SourcePathLink
        projectId="p1"
        path="pkg/x.go"
        line={4}
        rootRefs={[{ id: "root-1", path: "/proj" }]}
      />
    ));
    const el = screen.getByTestId("source-path-link");
    expect(el.getAttribute("data-project-id")).toBe("p1");
    expect(el.getAttribute("data-den-source-path")).toBe("pkg/x.go");
    expect(el.getAttribute("data-den-source-line")).toBe("4");
    expect(el.getAttribute("data-root-id")).toBe("root-1");
  });

  it("renders handle-only as non-button text", () => {
    render(() => <SourcePathLink projectId="p1" handle="h#1" />);
    expect(screen.queryByTestId("source-path-link")).toBeNull();
    expect(screen.getByText("h#1")).toBeTruthy();
  });

  it("renders openable=true path as a link and opens on click", async () => {
    render(() => (
      <SourcePathLink projectId="p1" path="pkg/x.go" line={4} openable={true} />
    ));
    fireEvent.click(screen.getByTestId("source-path-link"));
    expect(openSourceLocation).toHaveBeenCalledWith({
      intent: "permanent",
      projectId: "p1",
      path: "pkg/x.go",
      line: 4,
    });
  });

  it("renders openable=false (directory) as plain text, not a link", () => {
    render(() => (
      <SourcePathLink projectId="p1" path="pkg/sub" line={4} openable={false} />
    ));
    expect(screen.queryByTestId("source-path-link")).toBeNull();
    expect(screen.getByText("pkg/sub:4")).toBeTruthy();
  });
});

describe("SourceUrlLink", () => {
  beforeEach(() => {
    confirmAndOpenExternalLink.mockReset();
    confirmAndOpenExternalLink.mockResolvedValue(true);
  });

  it("confirms then opens external URL", async () => {
    render(() => <SourceUrlLink url="https://example.com" />);
    fireEvent.click(screen.getByTestId("source-url-link"));
    expect(confirmAndOpenExternalLink).toHaveBeenCalledWith(
      "https://example.com",
    );
    expect(screen.getByTestId("source-url-link").hasAttribute("data-tip")).toBe(false);
  });

  it("keeps the full URL available when the visible label differs", () => {
    render(() => (
      <SourceUrlLink url="https://example.com/long" label="Example" />
    ));
    expect(screen.getByTestId("source-url-link").getAttribute("data-tip")).toBe(
      "https://example.com/long",
    );
  });

  it("renders invalid href as plain text", () => {
    render(() => <SourceUrlLink url="not-a-url" />);
    expect(screen.queryByTestId("source-url-link")).toBeNull();
    expect(screen.getByText("not-a-url")).toBeTruthy();
  });
});
