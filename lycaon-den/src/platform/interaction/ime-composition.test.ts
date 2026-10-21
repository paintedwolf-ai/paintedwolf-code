import { readFileSync, readdirSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";
import * as ts from "typescript";
import { IME_COMPOSITION_KEY_CODE, isImeComposition } from "./ime-composition.ts";

const src = join(import.meta.dirname, "../..");
const HELPER = "platform/interaction/ime-composition.ts";

/** Reads of composition state that are not key events, with the reason. */
const NON_KEY_READS: Record<string, string> = {
  // A beforeinput InputEvent: composed text arrives through composition events.
  "components/search/SearchQueryBar.tsx": "InputEvent.isComposing in beforeinput",
};

/** Keyboard owners whose local Enter, Escape, Tab, navigation or command keys leave composition to the input method. */
const KEYBOARD_OWNERS = [
  "components/GitStrip.tsx",
  "components/chatview/Composer.tsx",
  "components/chatview/QueuePopover.tsx",
  "components/home/HomeView.tsx",
  "components/inline-rename/InlineRenameInput.tsx",
  "components/primitives/AnchoredSurface.tsx",
  "components/search/Crossbar.tsx",
  "components/search/SearchQueryBar.tsx",
  "components/search/search-result-navigation.ts",
  "components/settings/appearance/KeyboardSettingsPanel.tsx",
  "components/source/editor/editor-command-bridge.ts",
  "components/source/editor/editor-editing-style.ts",
  "components/source/secrets/MarkSecretDialog.tsx",
  "components/workflow/AskUserDock.tsx",
  "files/documents/document-editor-binding.ts",
  "files/editor/editor-symbol-navigation.tsx",
  "files/tree/FilesTreeRows.tsx",
  "files/tree/files-tree-keyboard.ts",
  "platform/interaction/focus-trap.ts",
  "platform/interaction/modal-focus-trap.ts",
  "shortcuts/dispatcher.ts",
];

function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) return entry.name === "node_modules" ? [] : sourceFiles(path);
    return /\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name) && !entry.name.endsWith(".d.ts") ? [path] : [];
  });
}

type Read = { file: string; line: number; text: string };

function compositionReads(file: string, source: string): { flags: Read[]; codes: Read[]; calls: number } {
  const ast = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, file.endsWith("x") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
  const flags: Read[] = [];
  const codes: Read[] = [];
  let calls = 0;
  const at = (node: ts.Node): Read => ({ file, line: ast.getLineAndCharacterOfPosition(node.getStart()).line + 1, text: node.getText() });
  const isKeyCode = (node: ts.Expression) => ts.isPropertyAccessExpression(node) && node.name.text === "keyCode";
  const isComposingCode = (node: ts.Expression) => ts.isNumericLiteral(node) && Number(node.text) === IME_COMPOSITION_KEY_CODE;
  const visit = (node: ts.Node) => {
    if (ts.isPropertyAccessExpression(node) && node.name.text === "isComposing") flags.push(at(node));
    if (ts.isBinaryExpression(node) && ((isKeyCode(node.left) && isComposingCode(node.right)) || (isComposingCode(node.left) && isKeyCode(node.right)))) codes.push(at(node));
    if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && node.expression.text === "isImeComposition") calls += 1;
    ts.forEachChild(node, visit);
  };
  visit(ast);
  return { flags, codes, calls };
}

const corpus = sourceFiles(src).map((path) => {
  const file = relative(src, path).split("\\").join("/");
  return { file, ...compositionReads(file, readFileSync(path, "utf8")) };
});

describe("input method composition", () => {
  it("counts both the composing flag and the legacy commit key code", () => {
    expect(isImeComposition({ isComposing: true, keyCode: 13 })).toBe(true);
    expect(isImeComposition({ isComposing: false, keyCode: IME_COMPOSITION_KEY_CODE })).toBe(true);
    expect(isImeComposition({ keyCode: IME_COMPOSITION_KEY_CODE })).toBe(true);
    expect(isImeComposition({ isComposing: false, keyCode: 13 })).toBe(false);
    expect(isImeComposition({})).toBe(false);
  });

  it("is read from key events only through isImeComposition", () => {
    const flags = corpus.filter(({ file }) => file !== HELPER && !(file in NON_KEY_READS)).flatMap(({ flags }) => flags);
    const codes = corpus.filter(({ file }) => file !== HELPER).flatMap(({ codes }) => codes);
    expect(flags, "read composition from a key event with isImeComposition").toEqual([]);
    expect(codes, "the legacy composition key code belongs to isImeComposition").toEqual([]);
    for (const file of Object.keys(NON_KEY_READS)) {
      expect(corpus.find((entry) => entry.file === file)?.flags.length, `${file}: stale non-key composition read`).toBeGreaterThan(0);
    }
  });

  it("guards every audited keyboard owner", () => {
    const unguarded = KEYBOARD_OWNERS.filter((file) => !corpus.some((entry) => entry.file === file && entry.calls > 0));
    expect(unguarded).toEqual([]);
  });
});
