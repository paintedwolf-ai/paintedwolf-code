---
name: hunt-a-regression-with-bisect
description: Find the commit that introduced a reproducible regression using git bisect.
---

# Hunt a regression with bisect

Use this workflow to turn "it broke sometime" into the exact commit, mechanically.

## Workflow

1. Establish the two endpoints with evidence — one commit where the behavior is verifiably good and one where it is verifiably bad. Run the check at both before starting; a bisect anchored on an assumption converges on a lie.
2. Write the check as a script that exits 0 on good, non-zero on bad, and 125 when the commit cannot be judged (build broken for unrelated reasons). The 125 skip is what keeps a messy history from derailing the search.
3. Run the search mechanically — `git bisect start`, mark the endpoints, then `git bisect run <script>`, joined with `&&` as one command rather than one turn per step. Manual stepping invites judgment drift; the scripted run makes every verdict repeatable.
4. Capture `git bisect log` as the evidence trail, and when the culprit commit is named, read its diff and explain *why* it breaks the behavior. A bisect that ends at a commit nobody can explain is not finished — re-check the endpoints and the script.
5. Always end with `git bisect reset` — including on failure and interruption — so the working tree returns to where the user left it.
6. Report the culprit commit, the mechanism of the breakage, and the bisect log. The fix is a separate decision made on that evidence.

## Boundaries

- Bisect only checks out history; it must not carry uncommitted work. If the tree is dirty with changes you did not author, stop and say so rather than stashing or discarding anything.
- The check script must not mutate shared state (databases, remote services) as it replays old commits; point it at disposable fixtures.
- Old commits execute old code, including old build scripts; treat repository history as untrusted input and run checks in the same confinement as any other build.
