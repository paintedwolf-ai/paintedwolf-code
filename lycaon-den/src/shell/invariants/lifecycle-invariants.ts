import { readSourceText } from "../../test/stylesheet-source.ts";
import { join } from "node:path";
import { expect } from "vitest";
import {
  denSrc,
  shellSource,
  type InvariantEntry,
} from "./common.ts";

function assertBoot01(): void {
  const boot = readSourceText(join(denSrc, "platform/connection/app-boot.ts"));
  expect(boot).toMatch(/lastActiveProjectId/);
  expect(boot).toMatch(/restoreLastActiveProject/);
}

function assertOpen01(): void {
  const shell = shellSource();
  expect(shell).toMatch(/ready\.mounted/);
  expect(shell).toMatch(/ready\.open/);
  expect(shell).toMatch(/<WorkspaceOpeningVeil\s+visible=\{!workspaceRevealed\(\)\}(?:\s|>)/);
  // The rail's workspace regions paint on the reveal frame.
  expect(shell).toMatch(/sessionsLoaded=\{sidebarChatsPublished\(\)\}/);
  expect(shell).toMatch(/const sidebarChatsPublished = \(\) =>\s*workspaceRevealed\(\) &&/);
  expect(shell).toMatch(/statusRevealed=\{workspaceRevealed\(\)\}/);
  const nav = readSourceText(join(denSrc, "components/nav/FocusedProjectNav.tsx"));
  expect(nav).toMatch(/data-presentation=\{props\.statusRevealed === false \? "preparing" : "published"\}/);
  // The session board and its git read are part of the open.
  for (const file of ["platform/connection/app-boot.ts", "components/shell/session-creation.ts"]) {
    const source = readSourceText(join(denSrc, file));
    const afterEnrich = source.slice(source.indexOf("afterEnrich:"), source.indexOf("onOpened:"));
    expect(afterEnrich).toMatch(/workspacePreparation\([^)]*\)\.run\(\s*"session-board"/);
  }
  const rail = readSourceText(join(denSrc, "components/nav/ChatTabRail.tsx"));
  expect(rail).toMatch(/usePresentationParticipant\(\s*"git-status"/);
  const columns = readSourceText(join(denSrc, "components/shell/ShellColumns.tsx"));
  const stageColumn = columns.slice(
    columns.indexOf('aria-label="Stage"'),
    columns.indexOf("export function ShellChatColumn"),
  );
  expect(stageColumn).not.toMatch(/ProjectLoadingStage/);

  const veil = readSourceText(
    join(denSrc, "components/shell/WorkspaceOpeningVeil.tsx"),
  );
  expect(veil).toMatch(/den-shell-workspace-veil/);

  const files = readSourceText(join(denSrc, "files/components/ProjectFilesView.tsx"));
  expect(files).toMatch(/name: "files-stage"/);
  expect(shell).toMatch(/workspacePreparation\(projectId\)\.ready\(\)/);
}

function assertFilesChat01(): void {
  const view = readSourceText(join(denSrc, "files/components/ProjectFilesView.tsx"));
  // Chat changes invalidate workspace resolution, not file acquisition.
  expect(view).toMatch(/const sourceSessionId = \(\) => untrack\(chatSessionId\);/);
  expect(view).toMatch(/createFilesWorkspace\(\{[^}]*chatSessionId/);
  expect(view).toMatch(/<FilesTree[\s\S]*?sessionId=\{filesSourceAddress\(\)\}/);
  // A prop taking the untracked read would freeze on the chat that mounted the stage.
  expect(view).not.toMatch(/=\{sourceSessionId\(\)\}/);
  const workspace = readSourceText(join(denSrc, "files/source/files-workspace.ts"));
  // Workspace identity is independent of the selected chat.
  const scopeKey = /const scopeKey = JSON\.stringify\(\[(.*)\]\);/.exec(workspace)?.[1];
  expect(scopeKey).toBeDefined();
  expect(scopeKey).not.toMatch(/session|chat/i);
  expect(workspace).toMatch(/const address = workspace\.session_scoped \? sessionId : undefined;/);
}

function assertView01(): void {
  const subject = readSourceText(join(denSrc, "platform/windows/window-subject.ts"));
  const registry = readSourceText(join(denSrc, "platform/windows/workspace-view-registry.ts"));
  const host = readSourceText(join(denSrc, "../src-tauri/src/item_windows.rs"));
  expect(subject).toMatch(/WindowSubject[\s\S]*viewId: string/);
  expect(registry).toMatch(/setCurrentWorkspaceContexts/);
  expect(host).toMatch(/Window \{view_id\}/);
}

function assertView02(): void {
  const items = readSourceText(join(denSrc, "../src-tauri/src/item_windows.rs"));
  const appearance = readSourceText(join(denSrc, "../src-tauri/src/window_appearance.rs"));
  expect(items).toMatch(/close_item_window/);
  expect(appearance).toMatch(/CloseRequested[\s\S]*main\.hide\(\)/);
}

export const BOOT_OPEN_INVARIANTS: InvariantEntry[] = [
  {
    id: "INV-BOOT-01",
    class: "required",
    structural: assertBoot01,
    note: "lastActiveProjectId wired in boot restore",
  },
  {
    id: "INV-OPEN-01",
    class: "required",
    structural: assertOpen01,
    note: "Workspace preparation is scoped to its project and reveals once its regions report content; rail chips and chat list paint on that frame",
  },
];

export const VIEW_FILES_INVARIANTS: InvariantEntry[] = [
  {
    id: "INV-FILES-01",
    class: "required",
    structural: assertFilesChat01,
    note: "Files addresses a chat, never keys on it; only a session-scoped workspace names one",
  },
  {
    id: "INV-VIEW-01",
    class: "required",
    structural: assertView01,
    note: "Peer views keep an immutable subject and host-assigned window slot",
  },
  {
    id: "INV-VIEW-02",
    class: "required",
    structural: assertView02,
    note: "Peer close is scoped; macOS main close hides instead of quitting",
  },
];

export const LIFECYCLE_INVARIANTS: InvariantEntry[] = [
  ...BOOT_OPEN_INVARIANTS,
  ...VIEW_FILES_INVARIANTS,
];
