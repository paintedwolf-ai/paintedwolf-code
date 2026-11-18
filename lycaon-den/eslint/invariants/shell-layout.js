const SRC = "src/**/*.{ts,tsx}";
const LAYOUT_READS = [
  "MemberExpression[property.name=/^(clientWidth|clientHeight|offsetWidth|offsetHeight)$/]",
  "CallExpression[callee.property.name='getBoundingClientRect']",
];
const SPLIT_ATTRIBUTES = [
  "Literal[value=/data-host-band|data-split-collapse/]",
  "TemplateElement[value.raw=/data-host-band|data-split-collapse/]",
  "JSXAttribute[name.name=/^data-(host-band|split-collapse)$/]",
  "Identifier[name='collapseTarget']",
];
const STATIC_COMMAND_CATALOG = "COMMANDS|COMMAND_BY_ID|APP_MENU_LAYOUT|APP_MENU_EXCLUDED|CROSSBAR_EXCLUDED|sharedDefaults|platformDefaults|defaultKeymap";

/** Shell, stage and layout modules each keep to their own seam. */
export default [
  {
    name: "transcript-blocks-are-total",
    message: "Transcript block building is total: return a fallback block, never throw.",
    files: ["src/chat/workflow/workflow-spans.ts"],
    syntax: ["ThrowStatement"],
    seed: { code: `export function build(): never { throw new Error("unknown span"); }` },
  },
  {
    name: "row-toolbar-stays-row-local",
    message: "Message actions act within their mounted row; windowing unmounts everything a document query would find.",
    files: ["src/components/transcript/MessageActions.tsx"],
    syntax: ["MemberExpression[object.name='document'][property.name=/^(querySelector|querySelectorAll|getElementById)$/]"],
    seed: { code: `export const row = () => document.querySelector(".bubble");` },
  },
  {
    name: "shell-dismisses-the-layout-tray",
    message: "The shell dismisses the layout tray (shell/layout-dock-dismiss.ts); LayoutDock adds no listener of its own.",
    files: ["src/components/shell/LayoutDock.tsx"],
    syntax: ["CallExpression[callee.property.name='addEventListener']"],
    seed: { code: `export const bind = () => document.addEventListener("pointerdown", () => undefined);` },
  },
  {
    name: "editor-theme-names-no-chord",
    message: "A key literal here bypasses user rebinding; declare the chord and route it through the editor command bridge.",
    files: ["src/components/source/editor/codemirror-theme.ts"],
    syntax: ["Property[key.name='key'][value.type='Literal']", "Property[key.name='key'][value.type='TemplateLiteral']"],
    seed: { code: `export const keymap = [{ key: "Mod-s", run: () => true }];` },
  },
  {
    name: "editor-themes-scale-with-text-size",
    message: "A px font size ignores the text-size setting; use a token or rem.",
    files: ["src/{components,files}/**/*.ts"],
    syntax: ["Property[key.name='fontSize'] > Literal[value=/^[\\d.]+px$/]"],
    seed: { file: "src/components/source/editor/codemirror-theme.ts", code: `export const theme = { ".cm-content": { fontSize: "13px" } };` },
  },
  {
    name: "context-drawers-stay-in-pane",
    message: "Context drawers render inside their pane without a portal or a modal focus trap.",
    files: [
      "src/components/shell/ContextDrawer.tsx",
      "src/components/worker/WorkersDrawer.tsx",
      "src/components/worklog/WorklogPanel.tsx",
    ],
    imports: [
      { from: "solid-js/web", names: ["Portal"] },
      { from: "src/components/primitives/ResidentPortal.tsx" },
      { from: "src/platform/interaction/modal-focus-trap.ts", names: ["createModalFocusTrap"] },
    ],
    seed: { file: "src/components/shell/ContextDrawer.tsx", code: `import { Portal } from "solid-js/web"; export { Portal };` },
  },
  {
    name: "files-return-chip-lives-in-the-titlebar",
    message: "The Files return chip mounts in the shell titlebar, out of Files layout flow.",
    files: ["src/files/components/ProjectFilesView.tsx", "src/files/tree/files-tree-layout.ts"],
    imports: [{ from: "src/components/shell/StageBackChip.tsx" }],
    syntax: ["Literal[value=/den-files-stage-gutter/]", "TemplateElement[value.raw=/den-files-stage-gutter/]"],
    seed: {
      file: "src/files/tree/files-tree-layout.ts",
      code: `import { StageBackChip } from "../../components/shell/StageBackChip.tsx"; export { StageBackChip };`,
    },
  },
  {
    name: "files-uses-no-browse-chrome",
    message: "Files keeps its controls in its own sidebar; it does not mount the shared browse chrome.",
    files: ["src/files/**/*.{ts,tsx}"],
    imports: [{ from: "src/components/browse/BrowseChrome*" }],
    seed: {
      file: "src/files/tree/files-tree-layout.ts",
      code: `import { BrowseChrome } from "../../components/browse/BrowseChrome.tsx"; export { BrowseChrome };`,
    },
  },
  {
    name: "panes-are-measured-by-an-observer",
    message: "Read pane extents from observeElementExtent; a layout read in a handler forces layout.",
    files: ["src/components/browse/BrowseStagePanel.tsx", "src/files/tree/files-tree-layout.ts"],
    syntax: LAYOUT_READS,
    seed: { file: "src/files/tree/files-tree-layout.ts", code: `export const width = (el: HTMLElement) => el.clientWidth;` },
  },
  {
    name: "layout-bands-observe-the-content-box",
    message: "Layout bands come from the shared content-box observer, never a direct width read.",
    files: ["src/layout/layout-bands.ts"],
    syntax: ["MemberExpression[property.name=/^(innerWidth|clientWidth)$/]", "CallExpression[callee.property.name='getBoundingClientRect']"],
    seed: { code: `export const width = () => window.innerWidth;` },
  },
  {
    name: "split-threshold-has-one-reader",
    message: "shell/stage-placement.ts compares a width to the split seam; the shell passes it a width.",
    files: ["src/components/shell/Shell.tsx"],
    imports: [{ from: "src/shell/stage-placement.ts", names: ["SPLIT_MIN_HOST"] }],
    seed: { code: `import { SPLIT_MIN_HOST } from "../../shell/stage-placement.ts"; export { SPLIT_MIN_HOST };` },
  },
  {
    name: "split-survivor-is-one-attribute",
    message: "A narrow split declares its surviving column with data-narrow-survivor alone.",
    files: [
      "src/components/shell/StageSplitHost.tsx",
      "src/components/shell/Shell.tsx",
      "src/shell/stage-placement.ts",
      "src/components/nav/ChatTabRail.tsx",
      "src/components/nav/StageEdgeControls.tsx",
    ],
    syntax: SPLIT_ATTRIBUTES,
    seed: { file: "src/shell/stage-placement.ts", code: `export const collapseTarget = "stage";` },
  },
  {
    name: "notification-cards-mount-in-top-chrome",
    message: "Host notification cards mount in the chat top chrome, never in the transcript view.",
    files: ["src/components/chatview/ChatView.tsx"],
    imports: [
      "NoticeRail",
      "NotificationStack",
      "NoProviderCard",
      "VerifyTestNudge",
      "SpendCeilingReachedNudge",
      "SpendCeilingApproachingNudge",
      "SessionProtectionBanners",
    ].map((card) => ({ from: `src/**/${card}.tsx` })),
    seed: { code: `import { NoticeRail } from "../NoticeRail.tsx"; export { NoticeRail };` },
  },
  {
    name: "height-toggles-share-one-motion",
    message: "Animate a measured height through ui/height-toggle-motion.ts.",
    files: [SRC],
    // Tab panels animate an existing clip.
    ignores: ["src/ui/height-toggle-motion.ts", "src/components/nav/TabBar.tsx"],
    syntax: ["CallExpression[callee.property.name='animate'] Property[key.name='height']"],
    seed: {
      file: "src/components/TaskCard.tsx",
      code: `export const open = (el: HTMLElement) => el.animate([{ height: "0px" }, { height: "40px" }], 120);`,
    },
  },
  {
    name: "shell-holds-no-command-catalog",
    message: "Commands, default chords and menu placements come from the contribution frame; the shell exports no static catalog.",
    files: [SRC],
    ignores: ["src/**/*.generated.{ts,tsx}"],
    syntax: [
      `ExportNamedDeclaration > VariableDeclaration > VariableDeclarator[id.name=/^(${STATIC_COMMAND_CATALOG})$/]`,
      "ExportNamedDeclaration > FunctionDeclaration[id.name=/^(sharedDefaults|platformDefaults|defaultKeymap)$/]",
      "ExportNamedDeclaration > TSTypeAliasDeclaration[id.name='CommandId']",
    ],
    seed: { file: "src/shortcuts/dispatcher.ts", code: `export const COMMANDS = [];` },
  },
  {
    name: "specs-scope-chat-stage-queries",
    message: "A document-wide query also matches hidden resident chats; scope it with the shared stage selectors.",
    files: ["e2e/**/*.spec.ts"],
    syntax: [
      "CallExpression[callee.object.name='page'][callee.property.name='getByTestId'][arguments.0.value=/^(chat-composer|chat-stream|composer-send)$/]",
      "CallExpression[callee.object.name='page'][callee.property.name='locator'][arguments.0.value=/^\\[data-testid=[\"']?(chat-composer|chat-stream|composer-send)/]",
      "CallExpression[callee.object.name='page'][callee.property.name='locator'][arguments.0.quasis.0.value.raw=/^\\[data-testid=[\"']?(chat-composer|chat-stream|composer-send)/]",
    ],
    seed: {
      file: "e2e/boot.spec.ts",
      code: `import { test } from "@playwright/test"; test("seed", async ({ page }) => { await page.getByTestId("chat-composer").click(); });`,
    },
  },
];
