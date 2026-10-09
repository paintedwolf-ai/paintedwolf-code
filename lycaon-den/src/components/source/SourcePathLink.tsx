import { Show, createMemo, createSignal, onCleanup } from "solid-js";
import { openSourceLocation, sourceEntryKind, type OpenSourceLocationArgs } from "../../platform/navigation/open-source.ts";
import {
  resolveProjectFile,
  relativeUnderRoot,
  type ResolveProjectRoot,
} from "../../api/project-path.ts";
import { ContextMenu, type ContextMenuItem } from "../ContextMenu.tsx";
import {
  projectPathChatRef,
  projectPathMenuItems,
} from "../project-path-menu-items.ts";
import { cn } from "../../shared/cn.ts";
import { clickSelectedText } from "../../platform/interaction/selection-gesture.ts";
import { useSourceContext } from "./annotations/source-context.ts";
import { useChatDestinationScope } from "../../chat/composer/chat-destination-scope.tsx";
import type { ChatDestination } from "../../chat/composer/shared-composer-document.ts";
import { startChatAttachmentDrag } from "../../chat/composer/chat-attachment-drag.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { useNoticesOptional } from "../../notices/notice-reporter.tsx";
import { beginSourceNavigation } from "../../platform/navigation/source-navigation-intent.ts";
import type { ScopeChangeKind } from "../../files/components/files-scope-change-mark.ts";

export type SourcePathVariant = {
  size?: "sm";
  truncate?: boolean;
  strong?: boolean;
  chip?: boolean;
};

export type SourcePathLinkProps = SourcePathVariant & {
  /** Overrides the surrounding source context. */
  projectId?: string;
  /** Limits resolution to one attached root. */
  rootId?: string;
  /** Absolute, root-relative, or root-qualified path; absent paths render as labels. */
  path?: string;
  entryKind?: "file" | "folder" | "unknown";
  line?: number;
  /** Inclusive end line when opening a selection range. */
  endLine?: number;
  handle?: string;
  /** Overrides the path, line, or handle label. */
  label?: string;
  rootRefs?: readonly ResolveProjectRoot[];
  /** Null omits Add to chat; absent uses the surrounding chat scope. */
  chatDestination?: ChatDestination | null;
  /** Host-verified openability; false renders plain text. */
  openable?: boolean;
  /** Change colours apply only to plain labels (INV-SRC-12). */
  change?: ScopeChangeKind | null;
  /** Worker overlay; null selects the project tree. */
  jobId?: string | null;
};

function variantClass(props: SourcePathVariant): string {
  return cn(
    props.size === "sm" && "den-source-path--sm",
    props.truncate && "den-source-path--truncate",
    props.strong && "den-source-path--strong",
    props.chip && "den-source-path--chip",
  );
}

function defaultLabel(
  path?: string,
  line?: number,
  endLine?: number,
  handle?: string,
): string {
  const p = path?.trim();
  if (!p) return handle?.trim() || "";
  if (line == null || !Number.isFinite(line)) return p;
  if (
    endLine != null &&
    Number.isFinite(endLine) &&
    endLine !== line
  ) {
    return `${p}:${line}-${endLine}`;
  }
  return `${p}:${line}`;
}

export function SourcePathLink(props: SourcePathLinkProps) {
  const source = useSourceContext();
  const notices = useNoticesOptional();
  const projectId = () => (props.projectId ?? source?.projectId ?? "").trim();
  const rootRefs = () => props.rootRefs ?? source?.rootRefs;
  const jobId = () =>
    props.jobId === undefined ? source?.jobId : props.jobId ?? undefined;
  const scopedDestination = useChatDestinationScope();
  const chatDestination = () =>
    props.chatDestination === undefined ? scopedDestination() : props.chatDestination;

  const path = () => props.path?.trim() ?? "";
  const label = () =>
    props.label?.trim() ||
    defaultLabel(props.path, props.line, props.endLine, props.handle);
  const [menu, setMenu] = createSignal<{
    anchor: { x: number; y: number };
    items: ContextMenuItem[];
  } | null>(null);
  let menuGeneration = 0;
  onCleanup(() => { menuGeneration++; });

  const openTarget = (): OpenSourceLocationArgs => ({
    intent: "permanent",
    projectId: projectId(),
    rootId: resolvedPath()?.rootId ?? props.rootId,
    path: relativePath() ?? path(),
    entryKind: entryKind(),
    line: props.line,
    endLine: props.endLine,
    jobId: jobId(),
  });

  const open = () => {
    const p = path();
    if (!p || !projectId() || props.openable === false) return;
    if (rootRefs()?.length && !resolvedPath()) {
      beginSourceNavigation();
      notices.reportError(new Error(resolutionError() === "outside_roots"
        ? `${p} is outside the attached project folders. Attach its folder to this project to open it here.`
        : "This path is unavailable in the attached project folders."));
      return;
    }
    void openTargetLocation(openTarget());
  };

  const openTargetLocation = async (target: OpenSourceLocationArgs) => {
    try {
      const result = await (target.entryKind === "unknown"
        ? openSourceLocation(target, { client: getLycaonClient() })
        : openSourceLocation(target));
      if (result.status === "rejected") notices.reportError(new Error(result.reason));
      if (result.status === "noop") notices.reportError(new Error("This path is unavailable in the attached project folders."));
    } catch (error) { notices.reportError(error); }
  };

  const resolution = createMemo(() => {
    const p = path();
    const availableRoots = rootRefs() ?? [];
    const roots = props.rootId
      ? availableRoots.filter((root) => root.id === props.rootId)
      : availableRoots;
    if (!p || !roots.length) return undefined;
    return resolveProjectFile({ roots }, p);
  });
  const resolutionError = () => {
    const resolved = resolution();
    return resolved && "error" in resolved ? resolved.error : undefined;
  };
  const resolvedPath = () => {
    const resolved = resolution();
    return resolved && !("error" in resolved) ? resolved : undefined;
  };
  const absolutePath = () => resolvedPath()?.absolutePath;
  const relativePath = () => {
    const resolved = resolvedPath();
    const root = rootRefs()?.find((candidate) => candidate.id === resolved?.rootId);
    return root && resolved ? relativeUnderRoot(resolved.absolutePath, root.path) : undefined;
  };
  const entryKind = () => {
    if (relativePath() === ".") return "folder";
    return props.entryKind;
  };
  const chatRef = createMemo(() => {
    const abs = absolutePath();
    if (!abs || entryKind() === "unknown") return null;
    return projectPathChatRef({
      absolutePath: abs,
      rootId: props.rootId,
      jobId: jobId(),
      projectId: projectId(),
      entryKind: entryKind() === "folder" ? "folder" : "file",
      rootRefs: rootRefs() ?? [],
      chatDestination: chatDestination(),
    });
  });

  const openMenu = async (anchor: { x: number; y: number }) => {
    const resolved = resolvedPath();
    const relative = relativePath();
    if (!resolved || relative == null) return;
    const generation = ++menuGeneration;
    const target = openTarget();
    const refs = rootRefs() ?? [];
    const destination = chatDestination();
    try {
      const kind = target.entryKind === "unknown"
        ? await sourceEntryKind({ projectId: target.projectId, rootId: resolved.rootId, path: relative, jobId: target.jobId }, getLycaonClient())
        : target.entryKind ?? "file";
      if (generation !== menuGeneration) return;
      setMenu({ anchor, items: projectPathMenuItems({
        line: target.line, absolutePath: resolved.absolutePath,
        rootId: target.rootId, jobId: target.jobId, projectId: target.projectId,
        entryKind: kind, rootRefs: refs, chatDestination: destination,
        onOpen: () => openTargetLocation({ ...target, entryKind: kind }),
      }) });
    } catch (error) { if (generation === menuGeneration) notices.reportError(error); }
  };

  return (
    <>
      <Show
        when={path() && projectId() && props.openable !== false}
        fallback={
          <Show when={label()}>
            {(text) => (
              <span class={cn("den-source-path-plain", variantClass(props))} data-file-change={props.change ?? undefined}>
                {text()}
              </span>
            )}
          </Show>
        }
      >
        <button
          type="button"
          class={cn("den-source-path-link", variantClass(props))}
          data-testid="source-path-link"
          data-file-change={props.change ?? undefined}
          data-project-id={projectId() || undefined}
          data-den-source-path={relativePath() ?? (path() || undefined)}
          data-den-source-line={
            props.line != null && Number.isFinite(props.line)
              ? String(props.line)
              : undefined
          }
          data-den-source-end-line={
            props.endLine != null &&
            Number.isFinite(props.endLine) &&
            props.line != null &&
            props.endLine !== props.line
              ? String(props.endLine)
              : undefined
          }
          data-root-id={resolvedPath()?.rootId ?? props.rootId}
          draggable={chatRef() ? true : undefined}
          onDragStart={(event) => startChatAttachmentDrag(event, chatRef())}
          data-tip={props.truncate ? label() : undefined}
          data-tip-when-clipped={props.truncate ? "" : undefined}
          onClick={(e) => {
            e.stopPropagation();
            if (clickSelectedText(e, e.currentTarget)) return;
            open();
          }}
          onContextMenu={(e) => {
            const abs = absolutePath();
            if (!abs || !rootRefs()?.length) return;
            e.preventDefault();
            e.stopPropagation();
            void openMenu({ x: e.clientX, y: e.clientY });
          }}
        >
          {label()}
        </button>
      </Show>
      <Show when={menu()} keyed>
        {(state) => <ContextMenu anchor={state.anchor} items={state.items}
          onDismiss={() => { menuGeneration++; setMenu(null); }} />}
      </Show>
    </>
  );
}
