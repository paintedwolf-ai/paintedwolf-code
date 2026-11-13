# Writer/reader table, worked example

The table is built from all of the authority's paths at once: search once for the authority's writers and readers, then open every path the search named in one response. Include migrations, retries, cancellation, recovery, background jobs, imports, test fixtures, generated code, and admin paths. Caches, replicas, log lines, and UI projections may describe the authority; they belong in the table only once they can disagree with it and win.

## Stating the invariant

Compare `weak: checkpoint handling is reliable` with `strong: for every accepted checkpoint, exactly one terminal row is durable within one transaction of acceptance`. The weak sentence names no observable state, scope, or time, so no observation can falsify it. The strong sentence counts durable terminal rows per accepted checkpoint within one transaction, so a duplicate or a missing row falsifies it.

## The table

For the strong invariant above, the table over the checkpoint record that accepts and serves them:

| Path | Role | Precondition | Postcondition / assumption |
|---|---|---|---|
| `AcceptCheckpoint` | writer | no terminal row exists | exactly one terminal row |
| recovery replay | writer | process restarted mid-accept | idempotent on request id |
| `GET /checkpoints` | reader | — | assumes at most one terminal row |
| nightly reconciler | writer | — | **assumes it may fix duplicates** |

The reconciler row is the tell: a writer whose precondition admits the invariant is already broken — its postcondition assumes the state the invariant forbids and positions a repair after the fact.

## Walking the rows to a counterexample

Read the rows as an event ordering the design admits. The reconciler's postcondition says duplicates happen; until the nightly run repairs them, `GET /checkpoints` — which assumes at most one terminal row — observes a violation, so the invariant holds only eventually, through a repair path outside the accept transaction. The counterexample to record is the ordering that creates the second terminal row; if no declared row can produce it, an undeclared path exists and the table is incomplete — widen the search rather than declare the invariant proven.

When a candidate ordering cannot be constructed, name the specific guard that makes it impossible. "No current caller does this" is a fact about callers, not an enforced invariant; record the row as unproved.
