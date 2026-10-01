/* @refresh reload */
import "overlayscrollbars/overlayscrollbars.css";
import "./tokens.generated.css";
import "./tokens-derived.css";
import "./fonts/font-faces.generated.css";
import "./tailwind.css";
import "./global.css";
import "./components/source/reader/source-reader.css";
import "./markdown.css";
import "./platform/themed-scrollbars.css";
import "./components/home/home-alignment.css";
import "./accessibility-appearance.css";
import { installTranscriptSpacing } from "./chat/transcript/layout/transcript-spacing.ts";
import { render } from "solid-js/web";
import App from "./App.tsx";
import { syncOsColorScheme } from "./theme.ts";
import { setupProjectThumbnailThemeInvalidation } from "./home/thumbnail-store.ts";
import { setupThemedScrollbars } from "./platform/scrolling/themed-scrollbars.ts";
import { setupWebviewNavigationGuards } from "./platform/desktop/external-link.ts";
import {
  tagTauriPlatformClasses,
  setupWindowChrome,
  revealWindow,
} from "./platform/windows/window-chrome.ts";
import { dismissBootFallback } from "./platform/connection/boot-fallback.ts";
import { initContributionStore } from "./contributions/contribution-store.ts";
import { APP_SCOPE } from "./notices/notice-scope.ts";
import {
  getLycaonClient,
  getRegisteredNoticeStore,
} from "./platform/connection/app-connection.ts";
import { installMainThreadPerfObserver } from "./chat/stream/den-main-thread-perf.ts";
import { setupAccessibilityTextSize } from "./platform/desktop/accessibility-text-size.ts";
import { setupSystemAppearance } from "./platform/desktop/system-appearance.ts";
import { setupWheelGlideDiagnostics } from "./platform/scrolling/wheel-glide-diagnostics.ts";
import { setupScrollActivity } from "./platform/scrolling/scroll-activity.ts";
import { TooltipHost } from "./components/primitives/TooltipHost.tsx";
import { HelpMenuHost } from "./help/HelpMenuHost.tsx";

installTranscriptSpacing(document.documentElement);
syncOsColorScheme();
tagTauriPlatformClasses();
// Reveal after platform geometry is tagged; the host timer handles stalled boot.
void revealWindow();
// Contribution loading reads client and notice stores lazily.
initContributionStore(
  getLycaonClient,
  () => getRegisteredNoticeStore()?.reporterFor(APP_SCOPE) ?? null,
);
setupWebviewNavigationGuards();
setupScrollActivity();
// Active only when main-thread performance debugging is enabled.
installMainThreadPerfObserver();
void setupWindowChrome();
void setupAccessibilityTextSize();
void setupSystemAppearance();
void setupWheelGlideDiagnostics();

const root = document.getElementById("root");
if (!root) {
  throw new Error("Missing #root — cannot boot Painted Wolf Code");
}

try {
  render(
    () => (
      <>
        <App />
        <TooltipHost />
        <HelpMenuHost />
      </>
    ),
    root,
  );
  setupThemedScrollbars();
  setupProjectThumbnailThemeInvalidation();
  if (import.meta.env.VITE_LYCAON_PROXY === "1") {
    // Harness mode only: expose window.__harness for LLM-driven testing.
    // Tree-shaken out of production builds, where the flag is unset.
    void import("./platform/harness/harness-driver.ts").then((m) => m.installHarnessDriver());
  }
} catch (err) {
  const message = err instanceof Error ? err.message : String(err);
  root.textContent = `Boot failed: ${message}`;
  // The splash covers the error message until it is removed.
  void dismissBootFallback({ immediate: true });
  throw err;
}
