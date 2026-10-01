// Contract: docs/source-navigation.md

import {
  resolveProjectFile,
  relativeUnderRoot,
  type ResolveProjectFileInput,
} from "../../api/project-path.ts";
import { beginSourceNavigation } from "./source-navigation-intent.ts";
import type { LycaonClient } from "../../api/client.ts";

export type SourceNavigationAction = "open" | "reveal";
export type OpenSourceBufferIntent = "transient" | "permanent";

export type OpenSourceLocationArgs = {
  projectId: string;
  action?: SourceNavigationAction;
  entryKind?: "file" | "folder" | "unknown";
  /** Pinned attached root. */
  rootId?: string;
  /** Absolute, root-relative, or root-qualified path. */
  path?: string;
  line?: number;
  /** 1-based caret column. */
  column?: number;
  /** Inclusive end line when opening a selection range. */
  endLine?: number;
  /** Worker overlay containing the path. */
  jobId?: string;
  /** Buffer lifetime for internal opens. */
  intent: OpenSourceBufferIntent;
  /** Keyboard attention moves to the opened editor. */
  focus?: boolean;
};

export type OpenSourceRequest = {
  projectId: string;
  path: string;
  absolutePath: string;
  rootId: string;
  line?: number;
  column?: number;
  endLine?: number;
  jobId?: string;
  intent: OpenSourceBufferIntent;
  /** Keyboard attention moves to the opened editor. */
  focus?: boolean;
};

export type RevealSourceRequest = Pick<OpenSourceRequest, "projectId" | "rootId" | "path" | "absolutePath"> & {
  action: "reveal";
  entryKind: "file" | "folder";
};
export type SourceNavigationRequest = OpenSourceRequest | RevealSourceRequest;
export type OpenSourceSink = (req: SourceNavigationRequest) => void;

export type OpenSourceProjectLookup = (
  projectId: string,
) => ResolveProjectFileInput | undefined;

export type OpenSourceLocationResult =
  | { status: "opened-in-app" }
  | { status: "superseded" }
  | { status: "rejected"; reason: string }
  | { status: "noop"; reason: "no_path" | "no_project" | "resolve_failed" };

let openSourceSink: OpenSourceSink | null = null;
let pendingOpen: { request: SourceNavigationRequest; current: () => boolean } | null = null;
let projectLookup: OpenSourceProjectLookup | null = null;

export type OpenSourceOptions = {
  lookup?: OpenSourceProjectLookup;
  client?: Pick<LycaonClient, "browseProjectSource"> | null;
};

export function registerOpenSourceSink(sink: OpenSourceSink): () => void {
  openSourceSink = sink;
  if (pendingOpen) {
    const pending = pendingOpen;
    pendingOpen = null;
    if (pending.current()) sink(pending.request);
  }
  return () => {
    if (openSourceSink === sink) openSourceSink = null;
  };
}

export function registerOpenSourceProjectLookup(
  lookup: OpenSourceProjectLookup,
): () => void {
  projectLookup = lookup;
  return () => {
    if (projectLookup === lookup) projectLookup = null;
  };
}

function dispatchInApp(req: SourceNavigationRequest, current: () => boolean): void {
  if (openSourceSink) openSourceSink(req);
  else pendingOpen = { request: req, current };
}

export function resolveProjectPath(
  args: Pick<OpenSourceLocationArgs, "projectId" | "rootId" | "path">,
  lookup?: OpenSourceProjectLookup,
):
  | {
      status: "resolved";
      target: Pick<
        OpenSourceRequest,
        "projectId" | "path" | "absolutePath" | "rootId"
      >;
    }
  | { status: "noop"; reason: "no_path" | "no_project" | "resolve_failed" } {
  const projectId = args.projectId.trim();
  const path = (args.path ?? "").trim();
  if (!path) return { status: "noop", reason: "no_path" };
  if (!projectId) return { status: "noop", reason: "no_project" };
  const project = (lookup ?? projectLookup)?.(projectId);
  if (!project) return { status: "noop", reason: "no_project" };
  const requestedRootId = args.rootId?.trim();
  const resolveProject = requestedRootId
    ? { roots: project.roots.filter((root) => root.id === requestedRootId) }
    : project;
  const resolved = resolveProjectFile(resolveProject, path);
  if ("error" in resolved) return { status: "noop", reason: "resolve_failed" };
  const root = resolveProject.roots.find((root) => root.id === resolved.rootId);
  if (!root) return { status: "noop", reason: "resolve_failed" };
  const relativePath = relativeUnderRoot(resolved.absolutePath, root.path);
  if (relativePath == null) return { status: "noop", reason: "resolve_failed" };
  return {
    status: "resolved",
    target: {
      projectId,
      path: relativePath,
      absolutePath: resolved.absolutePath,
      rootId: resolved.rootId,
    },
  };
}

export function resolveSourceRequest(
  args: OpenSourceLocationArgs,
  lookup?: OpenSourceProjectLookup,
):
  | { status: "resolved"; request: OpenSourceRequest }
  | { status: "noop"; reason: "no_path" | "no_project" | "resolve_failed" } {
  const resolved = resolveProjectPath(args, lookup);
  if (resolved.status === "noop") return resolved;
  return {
    status: "resolved",
    request: {
      ...resolved.target,
      intent: args.intent,
      ...(args.line != null ? { line: args.line } : {}),
      ...(args.column != null ? { column: args.column } : {}),
      ...(args.endLine != null ? { endLine: args.endLine } : {}),
      ...(args.jobId?.trim() ? { jobId: args.jobId.trim() } : {}),
      ...(args.focus ? { focus: true } : {}),
    },
  };
}

/** Absent entries use file navigation to reach retained history. */
export async function sourceEntryKind(
  req: Pick<OpenSourceRequest, "projectId" | "rootId" | "path" | "jobId">,
  client?: OpenSourceOptions["client"],
): Promise<"file" | "folder"> {
  if (req.path === ".") return "folder";
  // The project tree cannot classify an isolated worker path.
  if (req.jobId) return "file";
  if (!client) throw new Error("The host is unavailable. Try opening this path again after reconnecting.");
  const slash = req.path.lastIndexOf("/");
  const dir = slash < 0 ? "." : req.path.slice(0, slash);
  const name = req.path.slice(slash + 1);
  const listing = await client.browseProjectSource(req.projectId, { rootId: req.rootId, dir });
  return listing.entries.find((entry) => entry.name === name)?.is_dir ? "folder" : "file";
}

/** Classifies common paths without requiring a host round-trip. */
export function classifyPathKind(path: string): "file" | "folder" | "unknown" {
  if (path === "." || path.endsWith("/")) return "folder";
  const slash = path.lastIndexOf("/");
  const leaf = slash >= 0 ? path.slice(slash + 1) : path;
  const dot = leaf.lastIndexOf(".");
  if (dot > 0 && dot < leaf.length - 1) return "file";
  if (dot === 0 && leaf.length > 1) return "file";
  return "unknown";
}

/** Opens only paths resolved inside attached roots. */
export async function openSourceLocation(
  args: OpenSourceLocationArgs,
  options?: OpenSourceOptions,
): Promise<OpenSourceLocationResult> {
  const navigation = beginSourceNavigation();
  const resolved = resolveSourceRequest(args, options?.lookup);
  if (resolved.status === "noop") {
    return { status: "noop", reason: resolved.reason };
  }
  const req = resolved.request;
  let entryKind = args.entryKind;
  // Resolution normalizes every root path to ".".
  if (req.path === ".") entryKind = "folder";
  if (entryKind === "unknown") {
    const fast = classifyPathKind(req.path);
    if (fast !== "unknown") entryKind = fast;
  }
  if (entryKind === "unknown" && !req.jobId) {
    if (args.action === "reveal") {
      entryKind = "file";
    } else {
      try {
        entryKind = await sourceEntryKind(req, options?.client);
        if (!navigation.current()) return { status: "superseded" };
      } catch (error) {
        if (!navigation.current()) return { status: "superseded" };
        return { status: "rejected", reason: error instanceof Error ? error.message : "Could not resolve this source path." };
      }
    }
  }

  if (args.action === "reveal" || entryKind === "folder") {
    if (req.jobId) return { status: "rejected", reason: "This location belongs to a worker workspace, outside the project tree." };
    dispatchInApp({ action: "reveal", projectId: req.projectId, rootId: req.rootId,
      path: req.path, absolutePath: req.absolutePath, entryKind: entryKind === "folder" ? "folder" : "file" }, navigation.current);
    return { status: "opened-in-app" };
  }

  dispatchInApp(req, navigation.current);
  return { status: "opened-in-app" };
}

export function resetOpenSourceForTests(): void {
  beginSourceNavigation();
  openSourceSink = null;
  pendingOpen = null;
  projectLookup = null;
}
