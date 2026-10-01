# Build a system design review

Enter when a proposed coding-system change crosses three or more components, a durable boundary, or a consequential failure domain and needs technical review. If only one implementation path changes, write a focused plan; if the choice itself is unresolved, pair this with an engineering decision brief.

1. Write a **review frame** with `design claim`, `scope`, `reviewers`, `decision status`, `constraints`, and `out of scope`. The design claim is one falsifiable sentence about the system property the design provides.
2. Build a **boundary inventory**. For every affected component record `responsibility`, `inputs`, `outputs`, `state owned`, `failure signal`, and `consumer`. Classify wire, database, user-repository, device configuration, and ephemeral surfaces before choosing compatibility behavior.
3. Draw a **current-to-target architecture** with three to nine primary nodes. Arrows state what crosses the boundary—event, request, artifact, or authority—not vague verbs such as “uses.” Separate control, data, and trust paths with labels or distinct line treatments.
4. Produce an **interface table** with exact request/event shape, producer, consumer, ordering, idempotency, timeout, and error contract for every new or changed seam. Put detailed schemas in an appendix; keep the consequential fields in the review.
5. Add a **decision log** with accepted, open, and rejected alternatives. Each row contains `id`, `decision`, `reason`, `tradeoff`, `status`, and `owner`. Never present an open design question as an accepted architecture fact.
6. Model **failure and recovery**. Cover partial writes, duplicate delivery, cancellation, retry, stale state, process loss, unavailable dependency, and rollback when they apply. For each, name detection, contained effect, recovery action, and evidence that recovery completed.
7. Define a **rollout contract** with phases, compatibility boundaries, observability, go/no-go thresholds, rollback trigger, and cleanup of the retired path. Do not leave a permanent dual path unless a durable consumer requires it.
8. Read [the system-design layout](references/build_a_system_design_review-layout.md), render the review, and inspect node alignment, arrow meaning, table overflow, and small text. Use `render_view` for a bounded review board; use a local print route for multi-page interface and failure appendices.
9. Return a **design-review handoff** with `design_claim`, `architecture`, `changed_interfaces`, `accepted_decisions`, `open_questions`, `failure_recovery`, `rollout`, `limits`, and the artifact or local route.

Worked interface row:

| Seam | Producer → consumer | Shape | Ordering/idempotency | Timeout/error |
|---|---|---|---|---|
| Workspace created | Task launcher → session ledger | `task_id`, `workspace_id`, `root`, `head` | One accepted event per task id | 5s; task remains `preparing` with typed cause |

If the architecture cannot be grounded in current source or an explicit proposal, label it `conceptual target` and list the unverified assumptions. A diagram communicates the model; it does not prove the running system follows it.
