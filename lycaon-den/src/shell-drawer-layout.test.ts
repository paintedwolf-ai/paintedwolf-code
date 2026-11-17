// @vitest-environment jsdom
import { readSourceText } from "./test/stylesheet-source.ts";

import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const root = dirname(fileURLToPath(import.meta.url));
const drawerCss =
  readSourceText(join(root, "drawer-domain.css"), "utf8") +
  readSourceText(join(root, "drawer-utilities.css"), "utf8");
const shellCss = readSourceText(join(root, "shell-domain.css"), "utf8");
const shellUtils = readSourceText(join(root, "shell-utilities.css"), "utf8");
const shellStyles = shellCss + shellUtils;

describe("den-shell drawer layout contract", () => {
  it("split columns fill their host, and mirroring moves the drawer and the Files tree toggle", () => {
    expect(shellStyles).toMatch(
      /\.den-split-col--dual \.den-shell-stage\s*\{[\s\S]*?position:\s*absolute[\s\S]*?inset:\s*0/,
    );
    expect(shellStyles).toMatch(
      /\.den-shell-stage-host--handoff > \.den-split-col\s*\{[\s\S]*?position:\s*absolute[\s\S]*?inset:\s*0/,
    );
    expect(drawerCss).toMatch(
      /\.den-shell--orientation-mirrored \.den-context-drawer\s*\{[\s\S]*?left:\s*0[\s\S]*?border-right:\s*1px solid var\(--den-line\)[\s\S]*?animation-name:\s*den-drawer-in-mirrored/,
    );
    // Header padding follows the shell inset.
    expect(drawerCss).toMatch(
      /\.den-shell\s*\{[\s\S]*?--den-context-drawer-header-pad-top:\s*calc\(\s*var\(--den-titlebar-inset\) \+ var\(--den-tabs-top-gap\)\s*\)/,
    );
    expect(drawerCss).toMatch(
      /\.den-custom-chrome:not\(\.den-tauri-macos\) \.den-shell,\s*\.den-tauri-macos\.den-custom-chrome\s*\.den-shell:not\(\.den-shell--orientation-mirrored\)\s*\{[\s\S]*?--den-context-drawer-header-pad-top:\s*var\(--den-tabs-top-gap\)/,
    );
    expect(drawerCss).toMatch(
      /@utility den-context-drawer-header\s*\{[\s\S]*?padding:\s*var\(--den-context-drawer-header-pad-top\) 12px var\(--den-tabs-top-gap\)/,
    );
    const filesDomainCss = readSourceText(join(root, "files-domain.css"), "utf8");
    const filesUtilityCss = readSourceText(join(root, "files-utilities.css"), "utf8");
    expect(filesDomainCss).toMatch(
      /\.den-shell--orientation-mirrored \.den-files-tab-strip__tree-toggle\s*\{[\s\S]*?order:\s*2[\s\S]*?border-left:\s*1px solid var\(--den-line\)/,
    );
    expect(filesUtilityCss).toMatch(
      /@utility den-files-stage\s*\{[\s\S]*?\.den-shell--orientation-mirrored \.project-files-view &\s*\{[\s\S]*?flex-direction:\s*row-reverse/,
    );
  });

  it("custom chrome drag region excludes interactive controls", () => {
    expect(shellCss).toMatch(/\.den-custom-chrome \{/);
    expect(shellCss).toMatch(
      /\.den-shell-chrome-drag-surface\[data-tauri-drag-region\][\s\S]*-webkit-app-region:\s*drag/,
    );
    expect(shellCss).toMatch(
      /\.den-shell-header-chat \.tabs[\s\S]*pointer-events:\s*none/,
    );
    expect(shellCss).toMatch(
      /\.den-shell-header-chat \.tabs__chip[\s\S]*pointer-events:\s*auto/,
    );
    expect(shellCss).toMatch(
      /\.den-custom-chrome \.den-shell-header-titlebar > \.den-shell-window-controls\s*\{[\s\S]*?pointer-events:\s*auto/,
    );
    expect(shellStyles).toMatch(
      /\.den-shell-header-chat\s+:is\(\.tabs__rail-leading, \.tabs__rail-trailing\)\s+:is\(\.den-shell-nav-sidebar-btn-expand, \.den-pane-toggle\)\s*\{[\s\S]*?width:\s*var\(--den-unified-chrome-row\)/,
    );
    expect(shellStyles).toMatch(
      /:is\(\.tabs__rail-leading, \.tabs__rail-trailing\) \.den-inset-icon-btn[\s\S]*-webkit-app-region:\s*no-drag/,
    );
  });

  it("keeps the main-nav collapse control on the reopen-control row on every stage", () => {
    expect(shellCss).toMatch(
      /\.den-custom-chrome\s+\.den-shell-aside-titlebar\s+\.den-shell-nav-sidebar-btn-collapse\s*\{[\s\S]*?position:\s*absolute;[\s\S]*?width:\s*var\(--den-titlebar-edge-control\)/,
    );
    expect(shellCss).toMatch(
      /\.den-custom-chrome\s+\.den-shell-aside-titlebar\s+\.den-shell-nav-sidebar-btn-collapse\s*\{[^}]*?top:\s*calc\(\s*var\(--den-pane-control-center\) - var\(--den-titlebar-edge-control\) \/ 2\s*\)/,
    );
    expect(shellCss).toMatch(/right:\s*8px/);
  });

  it("Tauri fullscreen overlays keep a full-bleed scrim with an overlay-scoped drag band", () => {
    const overlayCss = readSourceText(join(root, "global-components.css"), "utf8");
    expect(overlayCss).toMatch(/\.den-overlay\s*\{[\s\S]*?\binset:\s*0\b/);
    expect(overlayCss).toMatch(
      /\.den-tauri\s+\.den-overlay\s*\{[\s\S]*?--den-overlay-inset-top:\s*52px/,
    );
    expect(overlayCss).toMatch(
      /\.den-overlay__chrome-drag\s*\{[\s\S]*?height:\s*var\(--den-overlay-inset-top\)/,
    );
  });

  it("onboarding gate controls a fixed top drag band under custom chrome", () => {
    const gateCss = readSourceText(join(root, "onboarding-domain.css"), "utf8");
    expect(gateCss).toMatch(
      /\.den-custom-chrome\s+\.onboarding-gate\s*\{[\s\S]*?--onboarding-gate-inset-top:\s*52px/,
    );
    expect(gateCss).toMatch(
      /\.onboarding-gate__chrome-drag\s*\{[\s\S]*?height:\s*var\(--onboarding-gate-inset-top\)/,
    );
    expect(gateCss).toMatch(
      /\.onboarding-gate__chrome-drag\[data-tauri-drag-region\][\s\S]*-webkit-app-region:\s*drag/,
    );
  });

  it("window host drags by the whole window except the critical stop card", () => {
    const stopCss = readSourceText(join(root, "global-components.css"), "utf8");
    expect(stopCss).toMatch(
      /\.den-window-host__chrome-drag\s*\{[\s\S]*?\binset:\s*0;/,
    );
    expect(stopCss).toMatch(
      /\.den-window-host__chrome-drag\[data-tauri-drag-region\][\s\S]*-webkit-app-region:\s*drag/,
    );
    // One full-height surface carries the layout inside the scrollport.
    expect(stopCss).toMatch(
      /\.den-window-host__surface\s*\{[\s\S]*?position:\s*relative[\s\S]*?display:\s*flex[\s\S]*?flex-direction:\s*column[\s\S]*?min-height:\s*100%/,
    );
    // The centered card leaves a drag surface around it and remains clickable.
    expect(stopCss).toMatch(
      /\.den-window-host__surface > \.den-critical-stop\s*\{[\s\S]*?position:\s*relative[\s\S]*?z-index:\s*1[\s\S]*?flex:\s*0 0 auto[\s\S]*?margin:\s*auto[\s\S]*?-webkit-app-region:\s*no-drag/,
    );
  });

  it("onboarding gate top-anchors the setup column (no vertical recenter jump)", () => {
    const gateCss = readSourceText(join(root, "onboarding-domain.css"), "utf8");
    expect(gateCss).toMatch(
      /\.onboarding-gate__content\s*\{[\s\S]*?flex-direction:\s*column[\s\S]*?align-items:\s*center/,
    );
    expect(gateCss).toMatch(
      /\.onboarding-gate__inner\s*\{[\s\S]*?margin:\s*0 auto/,
    );
  });

  it("onboarding steps open with opacity-only fade", () => {
    const gateCss = readSourceText(join(root, "onboarding-domain.css"), "utf8");
    expect(gateCss).toMatch(
      /\.onboarding-gate__page\s*\{[\s\S]*?opacity:\s*0/,
    );
    expect(gateCss).toMatch(
      /\.onboarding-gate__page--open\s*\{[\s\S]*?opacity:\s*1/,
    );
    expect(gateCss).toMatch(
      /transition:\s*opacity 480ms cubic-bezier\(0\.33,\s*0,\s*0\.2,\s*1\)/,
    );
  });

  it("first-run pending stage suppresses the home shell while the gate loads", () => {
    const gateCss = readSourceText(join(root, "onboarding-domain.css"), "utf8");
    expect(gateCss).toMatch(/\.onboarding-first-run-pending\s*\{/);
    // Nothing to click while prefs load: the whole surface drags.
    expect(gateCss).toMatch(
      /\.onboarding-first-run-pending__chrome-drag\s*\{[\s\S]*?\binset:\s*0;/,
    );
    expect(gateCss).toMatch(
      /\.onboarding-first-run-pending__chrome-drag\[data-tauri-drag-region\][\s\S]*-webkit-app-region:\s*drag/,
    );
  });

  it("the header at the window's leading edge clears the traffic lights", () => {
    // Under custom chrome the titlebar leaves layout flow; the stage keeps its inset.
    expect(shellCss).toMatch(
      /\.den-custom-chrome \.den-shell-header-titlebar\s*\{[^}]*position:\s*absolute/s,
    );
    expect(shellCss).toMatch(
      /\.den-custom-chrome[\s\S]*\.den-shell-stage--pane:has\(\.den-browse-stage\)[\s\S]*min-height:\s*var\(--den-titlebar-inset\)/,
    );
    // Whichever header sits at the window's left edge clears the lights.
    expect(shellCss).toMatch(
      /\.den-custom-chrome \.den-shell-header--window-leading \.den-shell-header-titlebar[\s\S]*?padding-left:\s*var\(--den-traffic-light-clearance/,
    );
    expect(shellCss).toMatch(/--den-traffic-light-clearance:\s*78px/);
  });

  it("context drawer headers drag the window without swallowing their controls", () => {
    const contextDrawer = readSourceText(
      join(root, "components", "shell", "ContextDrawer.tsx"),
      "utf8",
    );
    expect(contextDrawer).toMatch(
      /<ChromeDragSurface class="den-context-drawer__chrome-drag"/,
    );
    expect(drawerCss).toMatch(
      /\.den-context-drawer__chrome-drag\s*\{[\s\S]*?position:\s*absolute[\s\S]*?inset:\s*0/,
    );
    // In-pane cover of the stage titlebar (`absolute`, not `fixed`).
    expect(drawerCss).not.toMatch(/position:\s*fixed/);
    expect(drawerCss).toMatch(
      /\.den-context-drawer__chrome-drag\[data-tauri-drag-region\][\s\S]*?-webkit-app-region:\s*drag/,
    );
    // Drag band is absolute; title/close stack above it.
    expect(drawerCss).toMatch(
      /@utility den-context-drawer-header \{[\s\S]*?position:\s*relative/,
    );
    expect(drawerCss).toMatch(
      /\.den-context-drawer-header > \*:not\(\.den-context-drawer__chrome-drag\)\s*\{[\s\S]*?position:\s*relative[\s\S]*?z-index:\s*1/,
    );
    expect(drawerCss).toMatch(
      /@utility den-context-drawer-close \{[\s\S]*?app-region:\s*no-drag/,
    );
  });

  it("offers the orientation setting and narrows chat gutters by band", () => {
    const settings = readSourceText(
      join(root, "components/settings/appearance/GeneralSettingsPanel.tsx"),
      "utf8",
    );
    expect(settings).toMatch(/testId="display-workspace-orientation"/);
    expect(shellCss).toMatch(/\[data-chat-band~="max-34rem"\]/);
    expect(shellCss).toMatch(/--den-chat-gutter-x:\s*12px/);
    expect(shellCss).toMatch(/\[data-chat-band~="max-32rem"\]/);
    expect(shellCss).toMatch(/--den-chat-gutter-x:\s*6px/);
    expect(shellCss).toMatch(/--den-chat-scrollbar-shift:\s*4px/);
    expect(shellCss).toMatch(/--den-tabs-top-gap:\s*4px/);
    expect(shellCss).toMatch(
      /--tabs-rail-side:\s*calc\(var\(--den-unified-chrome-row\) \+ 0\.125rem\)/,
    );
    // Narrow columns use the chat gutter without the opposite side-slot inset.
    const compactTier = shellCss.match(
      /\[data-chat-band~="max-34rem"\]\)\s*\{[\s\S]*?\n\}/,
    )?.[0];
    expect(compactTier).toMatch(
      /:has\(\.tabs__rail-trailing\):not\(\s*:has\(\.tabs__rail-leading\)\s*\)[\s\S]*?--tabs-strip-inset-start:\s*var\(--den-tabs-gutter-x\)/,
    );
    expect(compactTier).toMatch(
      /:has\(\.tabs__rail-leading\):not\(\s*:has\(\.tabs__rail-trailing\)\s*\)[\s\S]*?--tabs-strip-inset-end:\s*calc\(\s*var\(--den-tabs-gutter-x\)\s*\+\s*var\(--chat-scrollbar-shift\)\s*\)/,
    );
    // The rail gutter contracts past the chat gutter, and goes flush at the
    // narrowest column — that margin is width the four chips need.
    expect(compactTier).toMatch(/--den-tabs-gutter-x:\s*4px/);
    const flushTier = shellCss.match(
      /\[data-chat-band~="max-32rem"\]\)\s*\{[\s\S]*?\n\}/,
    )?.[0];
    expect(flushTier).toMatch(/--den-tabs-gutter-x:\s*0px/);
    // Side buttons retain their gutter and separation from the last chip.
    expect(flushTier).toMatch(
      /--den-tabs-slot-gutter-x:\s*var\(--den-chat-gutter-x\)/,
    );
    expect(flushTier).toMatch(/--den-tabs-slot-gap:\s*0\.375rem/);
    const components = readSourceText(join(root, "global-components.css"), "utf8");
    expect(components).toMatch(
      /\.tabs__rail-trailing\s*\{[\s\S]*?padding-right:\s*var\(--den-tabs-slot-gutter-x\)/,
    );
    expect(components).toMatch(
      /\.tabs__rail-leading\s*\{[\s\S]*?padding-left:\s*var\(--den-tabs-slot-gutter-x\)/,
    );
  });

  it("titlebar is a slim drag region with window controls; chat rail controls tabs", () => {
    const shell = ["Shell.tsx", "WorkspaceSidebar.tsx", "ChatWorkspace.tsx"].map((file) => readSourceText(join(root, "components/shell", file), "utf8")).join("\n");
    const columns = readSourceText(join(root, "components/shell/ShellColumns.tsx"), "utf8");
    const titlebar = readSourceText(
      join(root, "components/shell/ShellHeaderTitlebar.tsx"),
      "utf8",
    );
    const chat = readSourceText(join(root, "components/chatview/ChatView.tsx"), "utf8");
    const runtime = readSourceText(join(root, "platform/runtime.ts"), "utf8");
    expect(columns).toMatch(/ShellHeaderTitlebar/);
    expect(shell).toMatch(/ChatTabRail/);
    expect(shell).toMatch(/ChatTabChromeProvider/);
    expect(shell).toMatch(/showChatTabRail/);
    expect(readSourceText(join(root, "components/shell/shell-pane-visibility.ts"), "utf8")).toMatch(/navCollapsedPref\(\)/);
    // Hidden panes reopen from the stage title bar, which keeps its height.
    expect(shell).toMatch(/<ShellStageColumn[\s\S]*?back=\{titlebarBack\(\)\}\s+edges=\{stageEdges\(/);
    expect(columns).toMatch(/<ShellHeaderTitlebar back=\{props\.back\} edges=\{props\.edges\} windowEdges=\{props\.windowEdges\} \/>/);
    expect(titlebar).toMatch(/StageEdgeControls/);
    // ChatView mounts within the chat-stage provider.
    const stageColumnStart = columns.indexOf('aria-label="Stage"');
    const chatStageShow = columns.indexOf("export function ShellChatColumn");
    expect(stageColumnStart).toBeGreaterThan(-1);
    expect(chatStageShow).toBeGreaterThan(-1);
    // Pending chat surfaces need full column bounds.
    expect(readSourceText(join(root, "components/shell/shell-residency.ts"), "utf8")).toMatch(
      /const chatColDual = \(\) => chatStack\.pending\(\) != null/,
    );
    expect(columns.slice(stageColumnStart, chatStageShow)).not.toMatch(
      /ChatStage/,
    );
    expect(titlebar).toMatch(/ChromeDragSurface/);
    expect(titlebar).toMatch(/WindowControls/);
    expect(chat).toMatch(/useChatTabChrome/);
    expect(chat).toMatch(/setChatTabRailBindings/);
    const tabRail = readSourceText(join(root, "components/nav/ChatTabRail.tsx"), "utf8");
    // The conversation's edge claims draw its window controls; the rail only places them at its ends.
    expect(tabRail).not.toMatch(/WindowControls\b(?!Start|End)/);
    const workspace = readSourceText(join(root, "components/shell/ChatWorkspace.tsx"), "utf8");
    expect(workspace).toMatch(
      /<For each=\{windowEdgeClaims\("chat"\)[^}]*\}>[\s\S]*?<WindowControls \{\.\.\.claim\} \/>/,
    );
    expect(workspace).toMatch(/windowControlsStart=\{chatWindowControls\("left"\)\}/);
    expect(workspace).toMatch(/windowControlsEnd=\{chatWindowControls\("right"\)\}/);
    expect(tabRail).toMatch(
      /<BrowseOverflowMenu[\s\S]*?disabled=\{!canExportConversation\(\)\}/,
    );
    expect(runtime).toMatch(/usesCustomWindowChrome/);
  });
});
