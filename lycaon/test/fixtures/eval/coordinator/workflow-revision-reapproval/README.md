# Compatible release reapproval

Select a compatible production release for R17, then reconcile its approvals.

selection records one revision for each component in baseline.json. Eligible approvals in approvals.json must be passed and for the requested release and production environment. Every selected approval's dependency bindings must equal the selected revisions of those components. Record the selected revisions.
impact records retain when the selected revision and dependency bindings equal the baseline, otherwise renew. release index records the selected approval ID for renewed components and the baseline approval ID for retained components. Approval IDs are local to each component.

All supplied files are immutable. Deliver records through this workflow; do not create files. Cite the source records used for every terminal verdict.
