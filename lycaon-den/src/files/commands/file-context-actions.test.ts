// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { ProjectRoot, SourceChatLocation } from "../../api/types.ts";
import { setStatusChipNavigationSink } from "../../chat/status/status-navigation-sink.ts";
import { resetContributionStoreForTest } from "../../contributions/contribution-store.ts";
import { seedStockFrame } from "../../contributions/stock-frame-test.ts";
import { reportSurfaceFailure } from "../../notices/surface-failure.ts";
import { required } from "../../test/at.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { copyTextToClipboard } from "../../utils/clipboard.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { chatLocationsEpoch, resetChatLocationsForTests } from "../source/chat-locations.ts";
import { recordOutsideRoot, resetOutsideRootsForTests } from "../source/outside-namespace.ts";
import { createFileContextActions, createFilePathIntents } from "./file-context-actions.ts";
import { filesContextMenuItems } from "./project-files-context-menu.ts";

vi.mock("../../notices/surface-failure.ts", async importOriginal => ({
  ...await importOriginal<typeof import("../../notices/surface-failure.ts")>(),
  reportSurfaceFailure: vi.fn(),
}));
vi.mock("../../utils/clipboard.ts", async importOriginal => ({
  ...await importOriginal<typeof import("../../utils/clipboard.ts")>(),
  copyTextToClipboard: vi.fn(),
}));

const projectId = () => "project";
const filesRoots = (): ProjectRoot[] => [{
  id: "attached", label: "project", path: "/home/me/project", is_primary: true,
  kind: "attached", admitted: true, admission_code: null, added_at: "2026-01-01T00:00:00Z",
}];
const location: SourceChatLocation = {
  id: "loc", root_id: "outside", session_id: "chat", kind: "folder", label: "notes",
  anchor: "home/me/notes", path: "/home/me/notes", available: true, hidden: false,
};
function actionsFor(client: () => LycaonClient | null) {
  const activeBuffer = () => null;
  const paths = createFilePathIntents({
    projectId, filesRoots, activeBuffer, rootLabelFor: () => "project", openReviewRowFile: vi.fn(),
  });
  return createFileContextActions({
    projectId, client, filesRoots, activeBuffer, state: () => projectFilesState(projectId()),
    sourceSessionId: () => "chat", resolveRootId: id => id, startTreeCreate: () => undefined,
    ...paths,
    setMoveError: vi.fn(), setMoveSource: vi.fn(), duplicateEntry: vi.fn(), setTrashConfirm: vi.fn(),
    setRenaming: vi.fn(), collapseTreeAll: () => undefined, expandTreeAll: () => undefined,
    requestCloseTab: vi.fn(), requestCloseFamily: vi.fn(), openFilesList: vi.fn(),
    onOpenInNewWindow: () => undefined, onOpenFileInNewWindow: () => undefined,
    openFile: vi.fn(), runFileRevert: vi.fn(async () => undefined), revealInTree: vi.fn(),
    treeReveal: { action: vi.fn() }, editorContextActions: vi.fn(), setCtxMenu: vi.fn(),
  }).ctxActions();
}

afterEach(() => {
  setStatusChipNavigationSink(null);
  resetContributionStoreForTest();
  resetChatLocationsForTests();
  resetOutsideRootsForTests();
  vi.clearAllMocks();
});

it("opens approvals and refreshes the tree only after the host hides a location", async () => {
  seedStockFrame();
  recordOutsideRoot(projectId(), location.root_id);
  const navigate = vi.fn();
  setStatusChipNavigationSink(navigate);
  const updateSourceChatLocation = vi.fn<LycaonClient["updateSourceChatLocation"]>()
    .mockResolvedValueOnce({ ...location, hidden: true })
    .mockRejectedValueOnce(new Error("Location unavailable"));
  const connected = stubClient({ updateSourceChatLocation });
  let client: LycaonClient | null = connected;
  const actions = actionsFor(() => client);
  const items = filesContextMenuItems({
    surface: "tree-row", rootId: location.root_id, path: location.anchor, name: location.label,
    isDir: true, isRoot: true, outsideLocation: location.id,
  }, actions) ?? [];
  const select = (id: string) => required(required(items.find(item => item.testId === id)).onSelect)();

  select("files-ctx-manage-location-access");
  expect(navigate).toHaveBeenCalledExactlyOnceWith({ kind: "project-configuration", section: "approvals" });
  select("files-ctx-copy-path");
  expect(copyTextToClipboard).toHaveBeenCalledExactlyOnceWith(location.path);

  select("files-ctx-hide-location");
  expect(updateSourceChatLocation).toHaveBeenCalledExactlyOnceWith(projectId(), location.id, { hidden: true });
  expect(chatLocationsEpoch(projectId())).toBe(0);
  await vi.waitFor(() => expect(chatLocationsEpoch(projectId())).toBe(1));
  select("files-ctx-hide-location");
  await vi.waitFor(() => expect(reportSurfaceFailure).toHaveBeenCalledWith(
    expect.objectContaining({ code: "files_location_hide_failed" }), "Location unavailable", projectId(),
  ));
  expect(chatLocationsEpoch(projectId())).toBe(1);

  client = null;
  select("files-ctx-hide-location");
  expect(updateSourceChatLocation).toHaveBeenCalledTimes(2);
});

it("preserves an outside file's host address and unavailable state when opening it externally", () => {
  recordOutsideRoot(projectId(), location.root_id);
  const { openInTarget } = actionsFor(() => null);
  expect(openInTarget?.({
    surface: "tree-row", rootId: location.root_id, path: location.anchor + "/notes.txt", name: "notes.txt",
    isDir: false, isRoot: false, outsideLocation: location.id, deleted: true,
  })).toEqual({
    absolutePath: location.path + "/notes.txt", projectRoots: ["/home/me/project"], entryKind: "file",
    unavailable: "This file is not present on disk.",
  });
});
