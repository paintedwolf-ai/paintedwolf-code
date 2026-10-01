import type { ContextMenuItem } from "./ContextMenu.tsx";
import { bindContextAction, contextAction } from "./context-actions.ts";
import { confirmAndOpenExternalLink, externalLinkDestinationLabel, externalLinkDestinationUnavailable, isExternalLinkHref } from "../platform/desktop/external-link.ts";
import { copyTextToClipboard } from "../utils/clipboard.ts";

export function linkMenuItems(href: string): ContextMenuItem[] {
  if (!isExternalLinkHref(href)) return [];
  const url = href.trim();
  const unavailable = externalLinkDestinationUnavailable(url);
  return [
    contextAction("openIn", { testId: "open-in-menu", submenu: [bindContextAction({
      label: externalLinkDestinationLabel(url),
      testId: "open-in-browser",
      description: unavailable,
      onSelect: () => confirmAndOpenExternalLink(url),
    }, () => !externalLinkDestinationUnavailable(url))] }),
    contextAction("copyLink", { testId: "link-menu-copy", onSelect: () => copyTextToClipboard(url) }),
  ];
}
