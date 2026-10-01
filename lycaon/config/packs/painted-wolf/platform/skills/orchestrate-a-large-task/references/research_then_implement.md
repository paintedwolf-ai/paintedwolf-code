# Research then implement

**Entry check:** name one implementation decision you cannot make yet and the fact that would settle it. If you cannot name the decision, the research has no stopping condition and will wander.

## Workflow

1. List the open decisions as questions with an assignee. Separate current external claims ("what does this API return in v3?") from repository questions ("where is this validated today?") — they need different researchers.
2. Mark each question **blocking** or **supplemental**. Blocking means a dependent builder cannot start; supplemental means the builder has a safe default and can proceed while the answer arrives.
3. Dispatch the narrowest matching researcher, choosing `agent_type` from the spawn roster in your prompt. Recommend `research-current-information` for external questions and the native survey tools (`summarize`, bounded `grep` / `find` / `read`) for repository questions. When substantial blocking research informs a substantial build, use a researcher leg followed by a builder leg; the dependency makes them sequential, not one worker plus coordinator-only research.
4. Require each researcher to return facts with evidence handles or URLs, scope limits, conflicts found, and remaining uncertainty — not an implementation plan outside its assignment.
5. Synthesize the answers into a decision packet, which is the artifact this skill exists to produce:

   ```text
   Verified: httpkit 0.8 needs the async runtime at 1.38+ (vendor docs, dated)
   Decided:  upgrade the runtime first, in its own leg
   Rejected: pinning httpkit 0.7 — drops the tracing layer we need
   Bounds:   language baseline unchanged; no public API change
   Paths:    server/src/router.rs, server/manifest
   Open (nonblocking): whether to adopt the new extractor syntax
   ```

6. Put the material packet facts in each dependent task's `brief.known_facts`, bounds in `brief.constraints`, and sources in `brief.context_refs`. Workers never see the research transcript, so omitted facts do not reach them.
7. Recommend one implementation skill per brief when it fits, and keep goal, constraints, responsibility, and done criteria explicit regardless.
8. Integrate, then verify against the researched constraints — not merely that the code builds. A build proves compilation, not that you honored the version bound you researched.

## When research fails

Stop and report rather than proceeding when a blocking claim cannot be verified, when two authoritative sources conflict, or when the only supporting evidence is undated. Do not search toward a preferred answer, and do not let a builder guess at a blocking fact.

## Stopping rule

Research ends when every blocking question is answered or explicitly escalated. Supplemental questions may remain open — record them in the packet and continue. One re-dispatch per unanswered blocking question; a second failure is an escalation, not a third search.

## Report

The decision packet, which questions were blocking, what remains open, and the verification evidence against the researched constraints.

Run research and implementation in parallel only when the builder has a safe default and the research cannot invalidate work already underway. When both should run together, dispatch a role set and brief them to share via `record_finding` — see the sizing table in `references/orchestrate_a_large_task.md`.
