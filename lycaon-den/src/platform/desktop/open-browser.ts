/**
 * Open a browser after confirmation.
 */

import type { BrowserPreset } from "../../../shared/app-state-types.ts";
import { isTauriRuntime, tauriPlatform } from "../runtime.ts";

export type OpenInBrowserArgs = {
  url: string;
  browser: BrowserPreset;
  customBrowserCommand?: string;
};

export type OpenInBrowserOptions = {
  isTauri?: boolean;
  openUrl?: (url: string) => void | Promise<void>;
  invokeOpen?: (args: {
    url: string;
    preset: string;
    customTemplate?: string;
  }) => Promise<void>;
};

async function defaultInvoke(args: {
  url: string;
  preset: string;
  customTemplate?: string;
}): Promise<void> {
  const { invoke } = await import("@tauri-apps/api/core");
  await invoke("open_in_browser", {
    url: args.url,
    preset: args.preset,
    customTemplate: args.customTemplate ?? null,
  });
}

export async function openUrlInBrowser(
  args: OpenInBrowserArgs,
  options?: OpenInBrowserOptions,
): Promise<void> {
  const tauri =
    options?.isTauri !== undefined ? options.isTauri : isTauriRuntime();

  if (!tauri) {
    if (options?.openUrl) {
      await options.openUrl(args.url);
      return;
    }
    if (typeof window !== "undefined") {
      window.open(args.url, "_blank", "noopener,noreferrer");
    }
    return;
  }

  const invoke = options?.invokeOpen ?? defaultInvoke;
  await invoke({
    url: args.url,
    preset: args.browser,
    customTemplate: args.customBrowserCommand,
  });
}

export const DEFAULT_APPLICATION_LABEL = "Default application";

export function browserDestinationLabel(browser: BrowserPreset): string {
  return {
    "system-default": "Browser",
    chrome: "Google Chrome",
    firefox: "Firefox",
    safari: "Safari",
    custom: "Browser",
  }[browser];
}

export function browserDestinationUnavailable(
  prefs: Pick<OpenInBrowserArgs, "browser" | "customBrowserCommand">,
): string | undefined {
  if (prefs.browser === "custom" && !prefs.customBrowserCommand?.trim()) {
    return "Set a custom browser command in settings.";
  }
  if (prefs.browser === "safari" && isTauriRuntime() && tauriPlatform() !== "macos") {
    return "Safari is only available on macOS.";
  }
  return undefined;
}
