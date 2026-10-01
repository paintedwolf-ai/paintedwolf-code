import { openSourceLocation, type SourceNavigationAction } from "../../platform/navigation/open-source.ts";
import { clickSelectedText } from "../../platform/interaction/selection-gesture.ts";
import { escapeHtml } from "./html-escape.ts";
import {
  resolveProjectFile,
  type ResolveProjectRoot,
} from "../../api/project-path.ts";
import type {
  ContextMenuAnchor,
  ContextMenuItem,
} from "../../components/ContextMenu.tsx";
import { pathMenuItems } from "../../components/path-menu-items.ts";
import { projectPathMenuItems } from "../../components/project-path-menu-items.ts";
import type { ChatDestination } from "../composer/shared-composer-document.ts";
import {
  parseProsePathCandidate,
  type ProsePathTarget,
} from "./prose-path-parse.ts";


export function isProjectPathHref(href: string): boolean {
  const raw = href.trim();
  if (!raw) return false;
  if (raw.startsWith("#")) return false;

  const schemeMatch = raw.match(/^[a-z][a-z0-9+.-]*:/i);
  if (schemeMatch) {
    return ["file:", "source:"].includes(schemeMatch[0].toLowerCase());
  }
  return true;
}

type ParsedProjectPathHref = ProsePathTarget & {
  entryKind: "file" | "folder";
};

function parseProjectPathHref(href: string): ParsedProjectPathHref | undefined {
  let raw = href.trim();
  if (!raw) return undefined;

  // Jailed resolution receives a scheme-free path.
  if (raw.toLowerCase().startsWith("file://")) {
    raw = raw.slice("file://".length);
  }

  const entryKind = /[\\/]$/.test(raw) ? "folder" : "file";
  if (entryKind === "folder") {
    raw = raw.replace(/[\\/]+$/, "");
  }

  // Line ranges use the prose path grammar.
  const parsed = parseProsePathCandidate(raw);
  return parsed ? { ...parsed, entryKind } : undefined;
}

type MarkdownProjectPathLinkOptions = {
  projectId?: string;
  untrusted?: boolean;
  requireValidatedTarget?: boolean;
  target?: ProsePathTarget;
};

/** `labelHtml` contains sanitized HTML. */
export function renderProjectPathButton(opts: ProsePathTarget & {
  projectId: string;
  entryKind: "file" | "folder";
  labelHtml: string;
  labelText: string;
}): string {
  const jobAttr = opts.jobId ? ` data-den-source-job-id="${escapeHtml(opts.jobId)}"` : "";
  const referenceAttr = opts.reference ? ` data-den-navigation-reference="${escapeHtml(opts.reference)}"` : "";
  const lineAttr = opts.line ? ` data-den-source-line="${opts.line}"` : "";
  const rootAttr = opts.rootId
    ? ` data-den-project-root-id="${escapeHtml(opts.rootId)}"`
    : "";
  const endAttr =
    opts.endLine && opts.line && opts.endLine !== opts.line
      ? ` data-den-source-end-line="${opts.endLine}"`
      : "";
  const tipRange =
    opts.line == null
      ? ""
      : opts.endLine && opts.endLine !== opts.line
        ? `:${opts.line}-${opts.endLine}`
        : `:${opts.line}`;
  const tip = `${opts.path}${tipRange}`;
  const clippedAttr = opts.labelText.trim() === tip
    ? " data-tip-when-clipped"
    : "";
  return (
    `<button type="button" class="den-source-path-link"` +
    `${referenceAttr}${jobAttr} data-project-id="${escapeHtml(opts.projectId)}"` +
    ` data-den-source-path="${escapeHtml(opts.path)}"` +
    ` data-den-project-path-kind="${opts.entryKind}"${rootAttr}${lineAttr}${endAttr}` +
    ` data-tip="${escapeHtml(tip)}"${clippedAttr}>${opts.labelHtml}</button>`
  );
}

export function renderMarkdownProjectPathLink(
  href: string,
  text: string,
  options: MarkdownProjectPathLinkOptions,
): string {
  const parsed = options.target ?? parseProjectPathHref(href);
  if (!parsed) return text;

  if (
    !options.projectId ||
    options.untrusted ||
    (options.requireValidatedTarget && !options.target)
  ) {
    const clippedAttr = text.trim() === href.trim()
      ? " data-tip-when-clipped"
      : "";
    return `<span class="den-source-path-plain" data-tip="${escapeHtml(href)}"${clippedAttr}>${text}</span>`;
  }

  const target = options.target;

  return renderProjectPathButton({
    reference: target?.reference,
    jobId: target?.jobId,
    projectId: target?.projectId ?? options.projectId,
    rootId: target?.rootId,
    path: target?.path ?? parsed.path,
    entryKind: target?.entryKind ?? parsed.entryKind ?? "file",
    line: target?.line ?? parsed.line,
    endLine: target?.endLine ?? parsed.endLine,
    labelHtml: text,
    labelText: text,
  });
}

function sourcePathButtonFromEvent(
  event: Event,
  projectId?: string,
): {
  button: HTMLElement;
  path: string;
  projectId: string;
  rootId?: string;
  jobId?: string;
  entryKind: "file" | "folder";
  line?: number;
  endLine?: number;
} | null {
  const target = event.target;
  if (!(target instanceof Element)) return null;

  const button = target.closest("[data-den-source-path]");
  if (!(button instanceof HTMLElement)) return null;

  const path = button.getAttribute("data-den-source-path")?.trim();
  const id = (button.getAttribute("data-project-id") ?? projectId)?.trim();
  const lineAttr = button.getAttribute("data-den-source-line");
  const endAttr = button.getAttribute("data-den-source-end-line");
  const rootId = button.getAttribute("data-den-project-root-id")?.trim();
  const jobId = button.getAttribute("data-den-source-job-id")?.trim();
  const entryKind =
    button.getAttribute("data-den-project-path-kind") === "folder"
      ? "folder"
      : "file";
  if (!path || !id) return null;

  const lineNum = Number(lineAttr ?? "");
  const line = Number.isFinite(lineNum) && lineNum > 0 ? lineNum : undefined;
  const endNum = Number(endAttr ?? "");
  const endLine =
    Number.isFinite(endNum) && endNum > 0 ? endNum : undefined;
  return { button, path, projectId: id, rootId, jobId, entryKind, line, endLine };
}

export function onMarkdownProjectPathLinkClick(
  event: MouseEvent | KeyboardEvent,
  projectId?: string,
): boolean {
  if (event instanceof KeyboardEvent) {
    if (event.key !== "Enter" && event.key !== " ") return false;
    event.preventDefault();
  }

  const hit = sourcePathButtonFromEvent(event, projectId);
  if (!hit) return false;

  event.preventDefault();
  event.stopPropagation();
  if (event instanceof MouseEvent && clickSelectedText(event, hit.button)) return true;
  void openSourceLocation({
    intent: "permanent",
    projectId: hit.projectId,
    rootId: hit.rootId,
    jobId: hit.jobId,
    path: hit.path,
    entryKind: hit.entryKind,
    line: hit.line,
    endLine: hit.endLine,
  });
  return true;
}

/** Menus resolve only targets inside attached roots. */
export function markdownProjectPathLinkContextMenu(
  event: MouseEvent,
  opts: {
    projectId?: string;
    rootRefs?: readonly ResolveProjectRoot[];
    chatDestination?: ChatDestination | null;
    onReferenceAction?: (anchor: Element, referenceId: string, action: SourceNavigationAction) => void;
  },
): { anchor: ContextMenuAnchor; items: ContextMenuItem[] } | null {
  const anchor = event.target instanceof Element ? event.target.closest("[data-den-navigation-reference]") : null;
  const referenceId = anchor?.getAttribute("data-den-navigation-reference");
  const referenceMenu = () => {
    if (!anchor || !referenceId || !opts.onReferenceAction) return null;
    event.preventDefault();
    event.stopPropagation();
    return { anchor: { x: event.clientX, y: event.clientY }, items: pathMenuItems({
      absoluteTestId: "path-menu-copy-path", relativeTestId: "path-menu-copy-relative-path",
      open: { testId: "path-menu-open", onSelect: () => opts.onReferenceAction?.(anchor, referenceId, "open") },
      revealInTree: !anchor.getAttribute("data-den-source-job-id") ? {
        testId: "path-menu-reveal-tree", onSelect: () => opts.onReferenceAction?.(anchor, referenceId, "reveal"),
      } : undefined,
    }) };
  };
  const hit = sourcePathButtonFromEvent(event, opts.projectId);
  if (!hit) return referenceMenu();

  const refs = opts.rootRefs ?? [];
  if (refs.length === 0) return referenceMenu();

  const resolved = resolveProjectFile(
    {
      roots: hit.rootId ? refs.filter((ref) => ref.id === hit.rootId) : refs,
    },
    hit.path,
  );
  if ("error" in resolved) return referenceMenu();

  event.preventDefault();
  event.stopPropagation();

  return {
    anchor: { x: event.clientX, y: event.clientY },
    items: projectPathMenuItems({
      absolutePath: resolved.absolutePath,
      rootId: resolved.rootId,
      projectId: hit.projectId,
      entryKind: hit.entryKind,
      line: hit.line,
      jobId: hit.jobId,
      onRevealInTree: anchor && referenceId && opts.onReferenceAction
        ? () => opts.onReferenceAction?.(anchor, referenceId, "reveal") : undefined,
      rootRefs: refs,
      chatDestination: opts.chatDestination,
      onOpen: () => {
        if (anchor && referenceId && opts.onReferenceAction) { opts.onReferenceAction(anchor, referenceId, "open"); return; }
        void openSourceLocation({
          intent: "permanent",
          projectId: hit.projectId,
          rootId: hit.rootId,
          jobId: hit.jobId,
          path: hit.path,
          entryKind: hit.entryKind,
          line: hit.line,
          endLine: hit.endLine,
        });
      },
    }),
  };
}
