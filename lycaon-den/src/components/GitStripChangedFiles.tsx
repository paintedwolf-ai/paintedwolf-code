import { For, Show } from "solid-js";
import { Scrollport } from "./primitives/Scrollport.tsx";
import { SourcePathLink } from "./source/SourcePathLink.tsx";
import {
  gitChangeOpenTarget,
  gitDiffSideOpenTarget,
  openVerifiedFileTarget,
} from "../files/source/file-open-target.ts";
import { getLycaonClient } from "../platform/connection/app-connection.ts";
import type { GitFileEntry } from "../api/types.ts";
import type { ResolveProjectRoot } from "../api/project-path.ts";

type Props = {
  files: readonly GitFileEntry[];
  projectId?: string;
  sessionId?: string;
  repoId: string;
  worktreeId?: string;
  rootRefs?: readonly ResolveProjectRoot[];
};

function statusLabel(code: string): string {
  const xy = code.trim();
  if (xy === "??") return "new";
  if (xy.startsWith("R")) return "renamed";
  if (xy.startsWith("D") || xy.endsWith("D")) return "deleted";
  if (xy.startsWith("A")) return "added";
  return "modified";
}

function statusFileChange(code: string): "added" | "deleted" | undefined {
  const label = statusLabel(code);
  if (label === "deleted") return "deleted";
  return label === "new" || label === "added" ? "added" : undefined;
}

function rootsForFile(
  file: GitFileEntry,
  rootRefs: readonly ResolveProjectRoot[] | undefined,
): ResolveProjectRoot[] | undefined {
  const rid = file.root_id?.trim();
  if (!rid || !rootRefs?.length) return undefined;
  const match = rootRefs.filter((r) => r.id === rid);
  return match.length ? match : undefined;
}

async function openGitChangeDiff(
  projectId: string,
  repoId: string,
  worktreeId: string | undefined,
  file: GitFileEntry,
  sessionId?: string,
): Promise<void> {
  const client = getLycaonClient();
  if (!client || !repoId) return;
  const isStagedOnly =
    file.status.length >= 2 &&
    file.status[0] !== " " &&
    file.status[0] !== "?" &&
    file.status[1] === " ";
  const stage = isStagedOnly ? "staged" : "unstaged";
  const diff = await client.getGitDiff(projectId, repoId, {
    ...(sessionId ? { session_id: sessionId } : {}),
    ...(worktreeId ? { worktree_id: worktreeId } : {}),
    mode: stage,
    path: file.path,
  });
  const item = diff.files[0];
  if (!item) return;
  const target = gitDiffSideOpenTarget({
    projectId,
    repoId,
    worktreeId: diff.worktree_id ?? worktreeId,
    stage,
    side: "after",
    item,
    headSha: diff.head_sha,
  });
  if (target) await openVerifiedFileTarget(target, { client, sessionId });
}

export function GitStripChangedFiles(props: Props) {
  return (
    <Scrollport
      class="git-strip__files"
      contentAs="ul"
      contentClass="git-strip__files-content"
      data-testid="git-files"
    >
      <For each={props.files}>
        {(f) => {
          const attributed = () => (f.root_id ?? "").trim().length > 0;
          return (
            <li class="git-strip__file">
              <span class="git-strip__file-status">
                {statusLabel(f.status)}
              </span>
              <Show
                when={attributed() && props.projectId?.trim()}
                fallback={
                  <span
                    class="den-source-path-plain den-source-path--truncate"
                    data-testid="git-file-unlinked"
                    data-file-change={statusFileChange(f.status)}
                  >
                    {f.path}
                  </span>
                }
                keyed
              >
                {(pid) => {
                  const navigationTarget = () => {
                    const target = gitChangeOpenTarget({
                      projectId: pid,
                      repoId: props.repoId,
                      worktreeId: props.worktreeId,
                      file: f,
                    });
                    if (!target?.rootId) return undefined;
                    return {
                      project_id: pid,
                      root_id: target.rootId,
                      path: target.path,
                      entry_kind: "file" as const,
                      ...(target.repoId ? { repo_id: target.repoId } : {}),
                      ...(target.worktreeId ? { worktree_id: target.worktreeId } : {}),
                    };
                  };
                  return (
                    <>
                      <SourcePathLink
                        truncate
                        projectId={pid}
                        path={f.root_relative_path || f.path}
                        repoId={props.repoId || undefined}
                        worktreeId={props.worktreeId}
                        navigationTarget={navigationTarget()}
                        change={statusFileChange(f.status)}
                        rootRefs={rootsForFile(f, props.rootRefs)}
                        chatDestination={
                          props.sessionId?.trim()
                            ? {
                                projectId: pid,
                                sessionId: props.sessionId.trim(),
                              }
                            : null
                        }
                      />
                      <Show when={props.repoId}>
                        <button
                          type="button"
                          class="den-chat-content-link git-strip__file-diff"
                          data-testid="git-file-diff"
                          aria-label={`Open diff for ${f.path}`}
                          onClick={() =>
                            void openGitChangeDiff(
                              pid,
                              props.repoId,
                              props.worktreeId,
                              f,
                              props.sessionId,
                            )
                          }
                        >
                          Diff
                        </button>
                      </Show>
                    </>
                  );
                }}
              </Show>
            </li>
          );
        }}
      </For>
    </Scrollport>
  );
}
