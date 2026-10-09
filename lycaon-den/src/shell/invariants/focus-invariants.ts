import { readSourceText } from "../../test/stylesheet-source.ts";
import { join } from "node:path";
import { expect } from "vitest";
import {
  denSrc,
  shellSource,
  type InvariantEntry,
} from "./common.ts";

function assertFocusKey01(): void {
  const shell = shellSource();
  expect(shell).toMatch(/stabilizeActiveChat/);
  expect(shell).toMatch(/holdPresentedChat/);
  expect(shell).toMatch(/presentedChat/);
  expect(shell).toMatch(/readyChat/);
  expect(shell).toMatch(/const railStatusProject = createMemo/);
  expect(shell).toMatch(/<Show when=\{railStatusProject\(\)\} keyed>/);
  expect(shell).toMatch(/sessionId=\{railStatusSessionId\(projectId\)\}/);

  const files = readSourceText(join(denSrc, "files/components/ProjectFilesView.tsx"));
  const deck = readSourceText(join(denSrc, "files/components/FilesPaneDeck.tsx"));
  // The resident deck mounts one pane per primitive pane key, never per buffer object.
  expect(files).toMatch(/return filesPaneKey\(key, buf\.kind\);/);
  expect(files).toMatch(/activePane=\{activePane\(\) \?\? null\}/);
  expect(deck).toMatch(/<SurfaceDeck active=\{props\.activePane\}/);
  expect(deck).toMatch(/const parsed = parseFilesPaneKey\(paneKey\);/);
  expect(files + deck).not.toMatch(/return \{ key, kind: buf\.kind \}/);
  const paneKey = readSourceText(join(denSrc, "files/components/files-pane-key.ts"));
  expect(paneKey).toMatch(/kind: FileBufferKind,\n\): string \{/);

  const host = readSourceText(join(denSrc, "files/editor/files-editor-host.ts"));
  expect(host).toMatch(/softDetachFilesEditor/);
  expect(host).toMatch(/beginFilesEditorAttach/);
  expect(host).toMatch(/SourceEditorHandlers/);

  const hotExit = readSourceText(join(denSrc, "files/documents/files-hot-exit.ts"));
  expect(hotExit).toMatch(/from "\.\.\/editor\/files-editor-host\.ts"/);

  const focus = readSourceText(join(denSrc, "shortcuts/focus-region.ts"));
  expect(focus).toMatch(/"find"/);
  expect(focus).toMatch(/"settings"/);

  const composer = readSourceText(join(denSrc, "components/chatview/Composer.tsx"));
  expect(composer).toMatch(/activeElementInFocusRegion/);
  expect(composer).toMatch(/isFocusRegionMounted\("settings"\)/);
  expect(composer).toMatch(/isFocusRegionMounted\("find"\)/);
  expect(composer).not.toMatch(/querySelector\("\.den-settings-panel"\)/);
  expect(composer).not.toMatch(/findController\.isOpen/);
  expect(composer).not.toMatch(/closest\('\[data-testid="find-bar"\]'\)/);

  const findBar = readSourceText(join(denSrc, "find/FindBar.tsx"));
  expect(findBar).toMatch(/registerFocusRegion\("find"/);

  const settings = readSourceText(
    join(denSrc, "components/settings/SettingsStagePanel.tsx"),
  );
  expect(settings).toMatch(/registerFocusRegion\("settings"/);
}

export const FOCUS_INVARIANTS: InvariantEntry[] = [
  {
    id: "INV-FOCUS-KEY-01",
    class: "required",
    structural: assertFocusKey01,
    note: "Focus hosts use primitive/stabilized keyed Show identities; Files controls EditorView via host; composer respects focus regions",
  },
];
