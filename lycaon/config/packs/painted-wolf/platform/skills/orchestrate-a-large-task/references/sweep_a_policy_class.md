# Sweep a policy class

**Entry check:** one rule applied everywhere it holds — not one surface finished well. A single module or feature is `evolve-a-system`. A purely syntactic rewrite may be `apply-a-structural-codemod` end to end.

## Workflow

1. **State the rule as one testable sentence,** with its scope and its real exceptions. "No compatibility shims in internal packages" is a rule. "Clean up the code" is not, and cannot be swept.
2. **Confirm the rule against the project's own standard** before changing anything. A sweep that applies your reading of a policy over the project's is a large, confident, wrong change.
3. **Enumerate candidates from the system, not from memory.** Search structurally where the language allows — the symbol, the import, the registration, the file shape. Include code you did not write and code the rule is inconvenient for. A sweep with unstated omissions is worse than no sweep, because it reads as complete.
4. **Judge each candidate before editing it.** Some hits are legitimate exceptions. Keep them, and record why next to the thing itself wherever the project has a marker for that — not as a list in a report nobody reads twice.
5. **Fix in surgical, per-file edits.** Re-read each file immediately before changing it; other people and agents may be working in the same tree. Never drive a sweep from a bulk rewrite script — one wrong pattern corrupts every match at once, and the blast radius is the whole repository.
6. **Leave a guard.** A sweep without one is undone by the next contributor who has not read the policy. Write it with `design-and-debug-tests` so it enumerates from the system and fails on the class, not on today's instances.
7. **Verify the full set, then run the project's normal gate.** A partial sweep reported as complete is the failure this skill exists to prevent.

## Stopping rule

One rule per sweep. A second rule noticed along the way is a second sweep — record it and keep going. Widening mid-pass makes the change unreviewable and the guard unwritable.

## Boundaries

- Do not sweep a rule the project has not adopted.
- Do not quietly narrow scope to what was easy; state what you left and why.
- Do not edit files outside the rule's scope because they were nearby.
- Do not commit, revert, or reorganize other people's unrelated in-progress work as part of the pass.

## Report

The rule and its scope, how candidates were enumerated and how many were found, what changed, exceptions kept with their reason and where each is recorded, the guard and the mutation that proved it fires, and anything in scope you did not fix.
