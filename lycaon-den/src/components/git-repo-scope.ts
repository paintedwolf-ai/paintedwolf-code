import type { GitRepoEntry } from "../api/types.ts";

export type GitScope =
  | { kind: "all" }
  | { kind: "repo"; repoId: string }
  | { kind: "root"; rootId: string };

export function pinMatchesSet(
  repos: readonly GitRepoEntry[],
  pin: GitScope,
): boolean {
  if (pin.kind === "all") return true;
  if (pin.kind === "repo") {
    return repos.some((r) => r.available && r.repo_id === pin.repoId);
  }
  return repos.some(
    (r) => !r.available && r.root_ids.includes(pin.rootId),
  );
}

/** A valid pin takes priority over the chat's active repository. */
export function resolveScope(
  repos: readonly GitRepoEntry[],
  activeRepoId: string,
  pin: GitScope | null,
): GitScope {
  if (pin && pinMatchesSet(repos, pin)) {
    return pin;
  }
  const active = activeRepoId.trim();
  if (active && repos.some((r) => r.available && r.repo_id === active)) {
    return { kind: "repo", repoId: active };
  }
  const firstAvail = repos.find((r) => r.available && r.repo_id?.trim())?.repo_id;
  if (firstAvail) {
    return { kind: "repo", repoId: firstAvail };
  }
  const firstRoot = repos.find((r) => !r.available && r.root_ids[0]?.trim());
  if (firstRoot?.root_ids[0]) {
    return { kind: "root", rootId: firstRoot.root_ids[0] };
  }
  return { kind: "all" };
}

/** Sum staged+unstaged across every available repository. */
export function gitReposChangeCount(repos: readonly GitRepoEntry[]): number {
  let n = 0;
  for (const r of repos) {
    if (!r.available) continue;
    n += (r.staged_count ?? 0) + (r.unstaged_count ?? 0);
  }
  return n;
}

export function nonRepoRootId(entry: GitRepoEntry): string {
  return entry.root_ids[0]?.trim() ?? "";
}
