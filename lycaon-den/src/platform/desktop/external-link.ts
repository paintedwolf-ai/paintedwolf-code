import { confirmDestructive } from "../interaction/confirm-dialog.ts";
import { clickSelectedText } from "../interaction/selection-gesture.ts";
import type { BrowserPreset } from "../../../shared/app-state-types.ts";
import { ISSUES_URL, REPOSITORY_URL, WEBSITE_URL } from "../../../shared/brand.ts";
import {
  externalOpenPrefs,
  resolveExternalOpenPrefs,
} from "../../settings/editor/external-open-prefs.ts";
import { browserDestinationLabel, browserDestinationUnavailable, DEFAULT_APPLICATION_LABEL, openUrlInBrowser } from "./open-browser.ts";

const EXTERNAL_LINK_TITLE = "Open external link";
const EXTERNAL_LINK_WARNING =
  "This link has not been validated. Open it outside Painted Wolf Code?";

let nativeWindowOpen: typeof window.open | null = null;
let guardAbort: AbortController | null = null;
let windowOpenGuardInstalled = false;

/** True for http(s), mailto, and tel destinations opened outside the app shell. */
export function isExternalLinkHref(href: string): boolean {
  const trimmed = href.trim();
  if (!trimmed) return false;
  const lower = trimmed.toLowerCase();
  return (
    lower.startsWith("http://") ||
    lower.startsWith("https://") ||
    lower.startsWith("mailto:") ||
    lower.startsWith("tel:")
  );
}

export function externalLinkConfirmMessage(href: string): string {
  return `${EXTERNAL_LINK_WARNING}\n\n${href.trim()}`;
}

function isWebLink(href: string): boolean {
  return /^https?:/i.test(href.trim());
}

export function externalLinkDestinationLabel(
  href: string,
  browser: BrowserPreset = externalOpenPrefs().browser,
): string {
  return isWebLink(href) ? browserDestinationLabel(browser) : DEFAULT_APPLICATION_LABEL;
}

export function externalLinkDestinationUnavailable(href: string): string | undefined {
  return isWebLink(href) ? browserDestinationUnavailable(externalOpenPrefs()) : undefined;
}

export function externalLinkOkLabel(href: string, browser?: BrowserPreset): string {
  const destination = externalLinkDestinationLabel(href, browser);
  const label = destination === "Browser" || destination === DEFAULT_APPLICATION_LABEL
    ? destination.toLowerCase() : destination;
  return `Open in ${label}`;
}

export type OpenInBrowserOptions = {
  browser?: BrowserPreset;
  customBrowserCommand?: string;
  isTauri?: boolean;
  openUrl?: (url: string) => void | Promise<void>;
};

/** The caller supplies confirmation before opening. */
export async function openInBrowser(
  url: string,
  options?: OpenInBrowserOptions,
): Promise<void> {
  const prefs = resolveExternalOpenPrefs(externalOpenPrefs());
  const browser = options?.browser ?? prefs.browser;
  const custom =
    options?.customBrowserCommand ?? prefs.customBrowserCommand;

  await openUrlInBrowser(
    {
      url,
      browser,
      ...(custom !== undefined ? { customBrowserCommand: custom } : {}),
    },
    {
      isTauri: options?.isTauri,
      openUrl:
        options?.openUrl ??
        ((href) => {
          const openFn =
            nativeWindowOpen ??
            (typeof window !== "undefined" ? window.open.bind(window) : null);
          openFn?.(href, "_blank", "noopener,noreferrer");
        }),
    },
  );
}

export type ConfirmAndOpenExternalLinkOptions = {
  confirm?: (message: string, okLabel: string) => boolean | Promise<boolean>;
  openInBrowser?: typeof openInBrowser;
  browser?: BrowserPreset;
};

/** Returns false for cancellation or an unsupported scheme. */
export async function confirmAndOpenExternalLink(
  href: string,
  options?: ConfirmAndOpenExternalLinkOptions,
): Promise<boolean> {
  const url = href.trim();
  if (!isExternalLinkHref(url)) return false;

  const prefs = resolveExternalOpenPrefs(externalOpenPrefs());
  const browser = isWebLink(url) ? options?.browser ?? prefs.browser : "system-default";
  const okLabel = externalLinkOkLabel(url, browser);
  const message = externalLinkConfirmMessage(url);

  const confirmed = options?.confirm
    ? await options.confirm(message, okLabel)
    : await confirmDestructive({
        message,
        title: EXTERNAL_LINK_TITLE,
        okLabel,
      });

  if (!confirmed) return false;

  const open = options?.openInBrowser ?? openInBrowser;
  await open(url, {
    browser,
    ...(prefs.customBrowserCommand !== undefined
      ? { customBrowserCommand: prefs.customBrowserCommand }
      : {}),
  });
  return true;
}

/** Destinations the app itself defines; only these open without the prompt. */
const APP_LINK_URLS: ReadonlySet<string> = new Set([WEBSITE_URL, REPOSITORY_URL, ISSUES_URL]);

/** Opens one of the app's own destinations in the preferred browser without confirmation; false for any other URL. */
export async function openAppLink(
  url: string,
  options?: Pick<ConfirmAndOpenExternalLinkOptions, "openInBrowser">,
): Promise<boolean> {
  if (!APP_LINK_URLS.has(url)) return false;
  const prefs = resolveExternalOpenPrefs(externalOpenPrefs());
  const open = options?.openInBrowser ?? openInBrowser;
  await open(url, {
    browser: prefs.browser,
    ...(prefs.customBrowserCommand !== undefined
      ? { customBrowserCommand: prefs.customBrowserCommand }
      : {}),
  });
  return true;
}

function handleExternalNavigation(href: string): void {
  void confirmAndOpenExternalLink(href);
}

function installWindowOpenGuard(): void {
  if (typeof window === "undefined") return;
  if (!windowOpenGuardInstalled) {
    nativeWindowOpen = window.open.bind(window);
    windowOpenGuardInstalled = true;
  }
  window.open = ((url?: string | URL) => {
    if (url != null) {
      const href = typeof url === "string" ? url : url.toString();
      if (isExternalLinkHref(href)) {
        handleExternalNavigation(href);
      }
    }
    return null;
  }) as typeof window.open;
}

function onAnchorNavigate(event: MouseEvent): void {
  if (event.defaultPrevented) return;
  const target = event.target;
  if (!(target instanceof Element)) return;
  const anchor = target.closest("a[href]");
  if (!(anchor instanceof HTMLAnchorElement)) return;
  const href = anchor.getAttribute("href");
  if (!href || !isExternalLinkHref(href)) return;
  event.preventDefault();
  event.stopPropagation();
  if (clickSelectedText(event, anchor)) return;
  handleExternalNavigation(href);
}

function onFormSubmit(event: Event): void {
  if (event.defaultPrevented) return;
  const form = event.target;
  if (!(form instanceof HTMLFormElement)) return;
  const action = form.action;
  if (!action || !isExternalLinkHref(action)) return;
  event.preventDefault();
  handleExternalNavigation(action);
}

/** Routes external navigation through confirmation and the browser preference. */
export function setupWebviewNavigationGuards(): void {
  if (typeof document === "undefined") return;
  guardAbort?.abort();
  guardAbort = new AbortController();
  const { signal } = guardAbort;
  installWindowOpenGuard();
  document.addEventListener("click", onAnchorNavigate, { signal });
  document.addEventListener("auxclick", onAnchorNavigate, { signal });
  document.addEventListener("submit", onFormSubmit, { signal });
}
