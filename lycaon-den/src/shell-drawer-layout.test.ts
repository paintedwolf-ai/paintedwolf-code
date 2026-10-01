// @vitest-environment jsdom
import { readSourceText } from "./test/stylesheet-source.ts";

import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { extractCssRuleBlock } from "./styling/css-contract.ts";

const root = dirname(fileURLToPath(import.meta.url));
const drawerCss =
  readSourceText(join(root, "drawer-domain.css"), "utf8") +
  readSourceText(join(root, "drawer-utilities.css"), "utf8");
const shellCss = readSourceText(join(root, "shell-domain.css"), "utf8");
const shellUtils = readSourceText(join(root, "shell-utilities.css"), "utf8");
const shellStyles = shellCss + shellUtils;

/** Block from `.den-context-drawer {` through its closing `}`. */
function workersDrawerBlock(): string {
  return extractCssRuleBlock(drawerCss, ".den-context-drawer");
}

describe("den-shell drawer layout contract", () => {
  it("context drawers stay within the chat pane and above its content", () => {
    const block = workersDrawerBlock();
    expect(block).toMatch(/top:\s*0/);
    expect(block).toMatch(/right:\s*0/);
    expect(block).toMatch(/border-left:\s*1px solid var\(--den-line\)/);
    expect(block).toMatch(/z-index:\s*var\(--den-z-context-drawer\)/);
    expect(block).toMatch(/position:\s*absolute/);
    expect(block).toMatch(/max-width:\s*100%/);
    expect(shellStyles).toMatch(
      /\.den-shell-header[\s\S]*z-index:\s*var\(--den-z-stage-header\)/,
    );
    expect(shellStyles).toMatch(
      /\.den-shell-stage--chat\s*\{\s*position:\s*relative/,
    );
    const chatStyles = readSourceText(join(root, "chat-utilities.css"), "utf8");
    const globalCssTokens = readSourceText(join(root, "global.css"), "utf8");
    expect(globalCssTokens).toMatch(/--den-z-stage-fill:\s*0/);
    expect(globalCssTokens).toMatch(/--den-z-content:\s*1/);
    expect(globalCssTokens).toMatch(/--den-z-stage-header:\s*4/);
    expect(globalCssTokens).toMatch(/--den-z-chat-chrome:\s*7/);
    expect(globalCssTokens).toMatch(/--den-z-composer-dock:\s*6/);
    expect(globalCssTokens).toMatch(/--den-z-context-drawer:\s*20/);
    expect(shellStyles).toMatch(
      /\.den-shell-stage--chat\s+\.den-shell-main\s*\{[\s\S]*?overflow:\s*visible/,
    );
    expect(shellStyles).toMatch(
      /\.den-shell-stage--chat\s+\.den-shell-main\s+>\s+\.den-chat\s*\{[\s\S]*?z-index:\s*var\(--den-z-composer-dock\)[\s\S]*?overflow:\s*visible/,
    );
    // Drawer-open chat stays below tab chrome.
    expect(chatStyles).toMatch(
      /\.den-shell-stage--chat[\s\S]*?>\s*\.den-chat\.den-chat--context-drawer-open\s*\{[\s\S]*?position:\s*static[\s\S]*?overflow:\s*visible[\s\S]*?z-index:\s*auto/,
    );
    expect(shellStyles).toMatch(
      /\.den-shell-stage-host \.den-shell-main > \*\s*\{[\s\S]*?z-index:\s*var\(--den-z-content\)/,
    );
    expect(chatStyles).toMatch(
      /@utility den-chat-composer-dock\s*\{[\s\S]*?z-index:\s*var\(--den-z-composer-dock\)/,
    );
    expect(chatStyles).toMatch(
      /@utility den-chat-conversation\s*\{[\s\S]*?z-index:\s*var\(--den-z-content\)/,
    );
    expect(shellStyles).toMatch(
      /\.den-shell-header-chat\s*\{[\s\S]*?z-index:\s*var\(--den-z-chat-chrome\)/,
    );
    expect(shellStyles).toMatch(
      /\.den-split-col--dual \.den-shell-stage\s*\{[\s\S]*?position:\s*absolute[\s\S]*?inset:\s*0/,
    );
    expect(shellStyles).toMatch(
      /\.den-shell-stage-host--handoff > \.den-split-col\s*\{[\s\S]*?position:\s*absolute[\s\S]*?inset:\s*0/,
    );
    expect(shellStyles).toMatch(
      /\.den-resident-surface--idle\s*\{[^}]*clip-path:\s*inset\(50%\)/,
    );
    expect(shellStyles).not.toMatch(
      /\.den-resident-surface--idle\s*\{[^}]*display:\s*none/,
    );
    expect(shellStyles).toMatch(
      /\.den-resident-surface--idle\s*\{[^}]*visibility:\s*hidden/,
    );
    expect(chatStyles).toMatch(
      /\.den-shell-main:has\(\.den-chat--context-drawer-open\)\s*\{[\s\S]*?position:\s*static[\s\S]*?overflow:\s*visible/,
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
      /\.den-custom-chrome \.den-shell-header-titlebar \.den-shell-window-controls[\s\S]*pointer-events:\s*auto/,
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
      /\.den-tauri-macos\.den-custom-chrome\s+\.den-shell-aside-titlebar\s+\.den-shell-nav-sidebar-btn-collapse\s*\{[\s\S]*?top:\s*calc\(\s*var\(--den-pane-control-center\) - var\(--den-titlebar-edge-control\) \/ 2\s*\)/,
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

  it("chat stage controls opaque fill under chrome (not host --chat)", () => {
    expect(shellCss).toMatch(
      /\.den-shell-stage--chat \.den-shell-header::before[\s\S]*?background:\s*var\(--den-background\)/,
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

  it("compact header layout applies outside custom chrome", () => {
    expect(shellCss).toMatch(/:root\s*\{[^}]*--shell-header-row-gap:/s);
    expect(shellCss).toMatch(
      /\.den-shell-header-chat[\s\S]*?padding:\s*calc\(var\(--den-titlebar-inset\) \+ var\(--den-tabs-top-gap\)\) 0 0/,
    );
    expect(shellCss).toMatch(
      /\.den-shell-header-chat[\s\S]*?min-height:\s*calc\([\s\S]*--den-tabs-top-gap[\s\S]*--den-unified-chrome-row\)/,
    );
    // The rail gutter contracts independently and clears the scrollbar.
    expect(shellCss).toMatch(
      /\.den-shell-header-chat \.tabs__rail[\s\S]*--tabs-strip-inset-start:\s*var\(--den-tabs-gutter-x\)/,
    );
    expect(shellCss).toMatch(
      /\.den-shell-header-chat \.tabs__rail[\s\S]*--tabs-strip-inset-end:\s*calc\(\s*var\(--den-tabs-gutter-x\)\s*\+\s*var\(--chat-scrollbar-shift\)\s*\)/,
    );
    expect(shellCss).toMatch(
      /\.den-shell-header-chat \.tabs__rail:has\(\.tabs__rail-leading\):not\(\s*:has\(\.tabs__rail-trailing\)\s*\)[\s\S]*--tabs-strip-inset-end:\s*calc\(\s*var\(--tabs-rail-side\)\s*\+\s*var\(--chat-scrollbar-shift\)\s*\)/,
    );
    // The dock spans the rail and aligns with chat content, not the chip strip.
    expect(shellCss).toMatch(
      /\.den-shell-header-chat \.tabs__dock\s*\{[^}]*padding-left:\s*var\(--den-chat-gutter-x\)[^}]*padding-right:\s*calc\(var\(--den-chat-gutter-x\) \+ var\(--den-chat-scrollbar-shift\)\)/,
    );
    // Panel geometry lives in shell-utilities (after global-components cascade).
    expect(shellUtils).toMatch(
      /\.den-shell-header-chat \.tabs__strip[\s\S]*padding-left:\s*var\(--tabs-strip-inset-start\)/,
    );
    expect(shellUtils).toMatch(
      /\.den-shell-header-chat \.tabs__strip[\s\S]*padding-right:\s*var\(--tabs-strip-inset-end\)/,
    );
    expect(shellUtils).toMatch(
      /\.den-shell-header-chat \.tabs__panel-shell[\s\S]*z-index:\s*2/,
    );
    // The backing spans the rail's side-control slots.
    expect(shellUtils).toMatch(
      /\.den-shell-header-chat \.tabs__panel-wash\s*\{[^}]*position:\s*absolute[^}]*top:\s*100%[^}]*left:\s*0[^}]*right:\s*calc\([\s\S]*var\(--den-chat-gutter-x\) - var\(--den-chat-scrollbar-shift\) \+[\s\S]*var\(--den-scrollbar-size\)/,
    );
    // The fade shares the backing width.
    expect(shellUtils).toMatch(
      /\.den-shell-header-chat \.tabs__panel-fade\s*\{[^}]*left:\s*0/,
    );
    expect(shellUtils).toMatch(
      /\.den-shell-header-chat \.tabs__panel-fade\s*\{[^}]*right:\s*0[^}]*width:\s*100%/,
    );
    expect(shellUtils).toMatch(
      /\.den-shell-header-chat \.tabs__panel-shell[\s\S]*margin-right:\s*0/,
    );
    const componentsCssForFade = readSourceText(
      join(root, "global-components.css"),
      "utf8",
    );
    expect(componentsCssForFade).toMatch(
      /\.tabs__panel-shell\s*\{[^}]*position:\s*absolute[^}]*top:\s*100%/,
    );
    expect(componentsCssForFade).toMatch(/\.tabs__row\s*\{[^}]*position:\s*relative/);
    expect(componentsCssForFade).toMatch(/\.tabs__panel-fade\s*\{[^}]*right:\s*0/);
    expect(componentsCssForFade).toMatch(
      /(?:^|\n)\.tabs__rail:not\(\.tabs__rail--compact\) \.tabs__chip--shown\s*\{[^}]*font-weight:\s*600[^}]*border-bottom-color:\s*var\(--den-accent\)/,
    );
    expect(shellCss).toMatch(
      /\.den-shell-header-chat\s*\{[\s\S]*?padding:\s*calc\(var\(--den-titlebar-inset\) \+ var\(--den-tabs-top-gap\)\) 0 0/,
    );
    // Whichever header sits at the window's left edge clears the lights.
    expect(shellCss).toMatch(
      /\.den-custom-chrome \.den-shell-header--window-leading \.den-shell-header-titlebar[\s\S]*?padding-left:\s*var\(--den-traffic-light-clearance/,
    );
    expect(componentsCssForFade).toMatch(
      /@container den-tab-chips \(max-width:\s*28rem\)/,
    );
    expect(shellStyles).toMatch(/\.den-shell-header-titlebar[\s\S]*?flex:\s*1/);
    expect(shellCss).toMatch(/--den-traffic-light-clearance:\s*78px/);
  });

  it("empty tab panels share one open-height floor", () => {
    const globalCss = readSourceText(join(root, "global.css"), "utf8");
    const componentsCss = readSourceText(join(root, "global-components.css"), "utf8");
    expect(globalCss).toMatch(/--den-tabs-panel-min-height:\s*5\.5rem/);
    expect(componentsCss).toMatch(
      /\.tabs__panel\s*\{[\s\S]*?min-height:\s*var\(--den-tabs-panel-min-height\)/,
    );
  });

  it("context drawers share an in-pane primitive without global modality", () => {
    const contextDrawer = readSourceText(
      join(root, "components", "shell", "ContextDrawer.tsx"),
      "utf8",
    );
    expect(contextDrawer).not.toMatch(/Portal/);
    expect(contextDrawer).not.toMatch(/createModalFocusTrap/);
    expect(contextDrawer).toMatch(/class="den-context-drawer"/);
    const workers = readSourceText(
      join(root, "components", "worker", "WorkersDrawer.tsx"),
      "utf8",
    );
    const worklog = readSourceText(
      join(root, "components", "worklog", "WorklogPanel.tsx"),
      "utf8",
    );
    for (const drawer of [workers, worklog]) {
      expect(drawer).toMatch(/<ContextDrawer/);
      expect(drawer).not.toMatch(/Portal/);
      expect(drawer).not.toMatch(/bindPanelOutsideDismiss/);
      expect(drawer).not.toMatch(/createModalFocusTrap/);
    }
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

  it("limits outside dismissal to the transient Layout tray", () => {
    const shell = readSourceText(join(root, "components/shell/Shell.tsx"), "utf8");
    expect(shell).toMatch(/dismissLayoutDockOnOutsidePress/);
    expect(shell).toMatch(/open: \(\) => dockTray\(\) === "layout"/);
    expect(shell).toMatch(/dock: \(\) => navDockEl/);
    expect(shell).toMatch(/onDismiss: closeLayoutTray/);
  });

  it("adapts main navigation and chat spacing to the actual column", () => {
    const shell = readSourceText(join(root, "components/shell/Shell.tsx"), "utf8");
    const settings = readSourceText(
      join(root, "components/settings/appearance/GeneralSettingsPanel.tsx"),
      "utf8",
    );
    expect(shell).toMatch(/"den-shell--orientation-mirrored":\s*workspaceOrientationPref\(\) === "mirrored"/);
    expect(shell).toMatch(/<NavResizeHandle[\s\S]*side=\{workspaceOrientationPref\(\) === "mirrored" \? "right" : "left"\}/);
    // Mirroring keeps the resize handle on the edge shared with the stage.
    expect(shellUtils).toMatch(
      /\.den-shell--orientation-mirrored \.den-shell-aside\s*\{[\s\S]*?flex-direction:\s*row-reverse/,
    );
    expect(shellCss).toMatch(
      /\.den-shell-nav-resize--right::before\s*\{[\s\S]*?left:\s*0[\s\S]*?right:\s*auto/,
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
    const shell = readSourceText(join(root, "components/shell/Shell.tsx"), "utf8");
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
    expect(columns).toMatch(/<ShellHeaderTitlebar back=\{props\.back\} edges=\{props\.edges\} \/>/);
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
    expect(tabRail).toMatch(/tabBarTrailingControls/);
    expect(tabRail).toMatch(
      /<BrowseOverflowMenu[\s\S]*?disabled=\{!canExportConversation\(\)\}/,
    );
    expect(runtime).toMatch(/usesCustomWindowChrome/);
  });

  it("session workers drawer lives in its conversation's ChatView", () => {
    const shell = readSourceText(join(root, "components/shell/Shell.tsx"), "utf8");
    expect(shell).not.toMatch(/SessionWorkersDrawer/);
    const chat = readSourceText(join(root, "components/chatview/ChatView.tsx"), "utf8");
    expect(chat).toMatch(/SessionWorkersDrawer/);
  });

  it("Shell controls the workers drawer state while ChatView controls its placement", () => {
    const shell = readSourceText(join(root, "components/shell/Shell.tsx"), "utf8");
    expect(shell).toMatch(/closeWorkers/);
    expect(shell).toMatch(
      /workersDrawerOpen=\{body\.surfaceActive\(\) && workersOpen\(\)\}/,
    );
  });

  it("worker roster lives in the Workers tab", () => {
    const tabRail = readSourceText(join(root, "components/nav/ChatTabRail.tsx"), "utf8");
    expect(tabRail).toMatch(/WorkersTab/);
    expect(tabRail).toMatch(/sessionWorkerRows/);
    expect(tabRail).toMatch(/liveWorkerRows/);
  });

  it("workers drawer renders WorkerTranscript", () => {
    const src = readSourceText(join(root, "components/worker/WorkersDrawer.tsx"), "utf8");
    expect(src).toMatch(/WorkerTranscript/);
  });

  it("patch-first SSE topics do not invalidate HTTP refetch", () => {
    const events = readSourceText(join(root, "api/events.ts"), "utf8");
    expect(events).toMatch(/message:\s*\[\]/);
    expect(events).toMatch(/worker:\s*\[\]/);
    expect(events).toMatch(/board:\s*\[\]/);
  });

  it("HTTP transcript hydrate lives in SessionWorkersDrawer", () => {
    const drawer = readSourceText(
      join(root, "components/worker/SessionWorkersDrawer.tsx"),
      "utf8",
    );
    expect(drawer).toMatch(/hydrateWorkerTranscripts/);
    expect(drawer).toMatch(/runTranscriptHydrate/);
    expect(drawer).toMatch(/workersColdCacheRevision/);
  });
});
