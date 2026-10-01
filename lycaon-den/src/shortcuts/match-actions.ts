import type { ContributionCommand } from "../api/types.ts";

export type MatchActionsOpts = {
  /** Availability-filtered candidates from the hydrated frame. */
  candidates: readonly ContributionCommand[];
  /** Optional recency boost, most-recent first. */
  recentIds?: readonly string[];
};

function haystacks(command: ContributionCommand): string[] {
  return [command.title, command.category ?? "", command.provider, ...(command.keywords ?? [])].map((s) =>
    s.toLowerCase(),
  );
}

export function splitQueryTerms(query: string): string[] {
  return query.trim().toLowerCase().split(/\s+/).filter(Boolean);
}

// Terms can match different command fields.
export function matchesAllTerms(
  haystacks: readonly string[],
  terms: readonly string[],
): boolean {
  return terms.every((term) => haystacks.some((h) => h.includes(term)));
}

function recencyRank(
  id: string,
  recentIds: readonly string[] | undefined,
): number {
  if (!recentIds || recentIds.length === 0) return Number.POSITIVE_INFINITY;
  const index = recentIds.indexOf(id);
  return index === -1 ? Number.POSITIVE_INFINITY : index;
}

function sortByRecency(
  rows: readonly ContributionCommand[],
  recentIds: readonly string[] | undefined,
): ContributionCommand[] {
  return [...rows].sort((a, b) => {
    const ra = recencyRank(a.id, recentIds);
    const rb = recencyRank(b.id, recentIds);
    if (ra !== rb) return ra - rb;
    if (a.title !== b.title) return a.title < b.title ? -1 : 1;
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  });
}

export function matchActions(
  query: string,
  opts: MatchActionsOpts,
): ContributionCommand[] {
  const terms = splitQueryTerms(query);
  if (!terms.length) return sortByRecency(opts.candidates, opts.recentIds);
  const matched = opts.candidates.filter((command) =>
    matchesAllTerms(haystacks(command), terms),
  );
  return sortByRecency(matched, opts.recentIds);
}
