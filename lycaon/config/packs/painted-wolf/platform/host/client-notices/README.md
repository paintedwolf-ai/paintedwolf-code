# Client notices

Copy for the failures **Den** reports about itself, keyed by `ClientNoticeKind`.

Distinct from `../user-notices/` by who can describe the condition, not by tone:
a user notice is rendered by the host and arrives over the wire, which is only
possible while the host is reachable. Every code here names a state where it is
not — the app cannot start, cannot connect, or is talking to a version that no
longer matches — so the words have to already be in the bundle.

That is why these are code-generated into `lycaon-den/src/notices/client-notices.generated.ts`
rather than fetched: `./task codegen:client-notices`, guarded by
`codegen:client-notices:check` in the check suite. Edit the YAML, never the
generated file.

`scope` is where the notice docks (`app`, `project`, `session`) — it lives here
so adding a code cannot leave the routing table behind.

`scenarios[].expect_contains` pins the phrases the copy must keep saying. It is
the reason a rewrite can be free: the words stay editable, the meaning does not.
