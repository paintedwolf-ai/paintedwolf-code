---
name: hunt-a-regression-with-bisect
description: Find the commit that introduced a reproducible regression with a scripted git bisect between a known-good and a known-bad commit.
---

# Hunt a regression with bisect

Use this workflow to turn "it broke sometime" into the exact commit, mechanically.

## Workflow

1. **Establish both endpoints with evidence.** Run the check at one commit where the behavior is verifiably good and one where it is verifiably bad. A bisect anchored on an assumption converges on the wrong commit.
2. **Write the check as a script in `@scratch`**, not in the repository, so checkouts cannot change it. It exits 0 on good, non-zero on bad, and 125 when a commit cannot be judged (for example an unrelated build break).
3. **Bisect in a separate worktree** so the user's tree and its uncommitted work never move:

   ```bash
   git worktree add @scratch/bisect <bad-commit>
   cd @scratch/bisect
   external -- git bisect start <bad-commit> <good-commit>
   external -- git bisect run sh @scratch/check.sh
   external -- git bisect log
   ```

   `git bisect` has no native form; `external -- git` runs the installed Git under the usual confinement. Run the steps as one program rather than one turn per step.
4. **Explain the culprit.** Read its diff (`git show <sha>`) and say why it breaks the behavior. A culprit nobody can explain means the endpoints or the script are wrong; re-check them.
5. **Clean up, including on failure:** `external -- git bisect reset`, then `git worktree remove @scratch/bisect`.
6. **Report** the culprit commit, the mechanism, and the bisect log. The fix is a separate decision made on that evidence.

## Boundaries

- Never bisect in the user's working tree, and never stash or discard their changes to make room.
- The check must not mutate shared state (databases, remote services) while replaying old commits; point it at disposable fixtures.
- Old commits run old code, including old build scripts. Treat history as untrusted input and run checks under the same confinement as any build.
