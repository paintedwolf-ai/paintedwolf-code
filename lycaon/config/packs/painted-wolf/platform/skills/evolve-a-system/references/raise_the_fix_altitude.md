# Raise the fix altitude

**Entry check:** you are holding several findings and about to write several fixes. A single finding needs no altitude check — fix it.

## Workflow

1. **Lay the findings out by mechanism, not symptom:** what each one touches, which layer it lives in, and what had to be true for it to happen.
2. **Look for the shared cause.** Findings collapse when they share a missing invariant, a fact every caller re-derives instead of being handed, a boundary that does not exist yet, a wrong default at the source, or a type that permits an invalid state. Repetition across unrelated files is the strongest signal: the same mistake made independently is usually the design inviting it.
3. **Name the one change that would make the class impossible,** and say which findings it removes and which it does not. Findings that survive are separate defects — do not stretch one story to cover them.
4. **Price it against fixing them individually.** A structural change earns its cost when it removes the class, deletes more than it adds, or makes the invalid state unrepresentable. Elegance alone does not earn it. Weigh what it touches, what must be retested, and the project's release stage — a wide change is cheap before release and expensive after.
5. **Present it as a decision.** One line for the structural option, one for the individual fixes, and your recommendation. Scope at this altitude belongs to the person responsible for the work.
6. **If the structural fix is taken, finish it.** Update every site the old shape reached and delete it; a structural change that leaves the old path alive has added a concept and removed nothing. Then prove the class with a guard so the findings cannot return one at a time.

## Stopping rule

Two passes. If a second look at the same findings produces no shared cause, they are genuinely separate — fix them individually and stop hunting for a theory.

## Boundaries

- Do not defer real bugs behind a redesign nobody has approved.
- Do not group findings by the file they landed in; that is proximity, not cause.
- Do not reach past the findings in hand to redesign something adjacent.
- One structural change per pass; a second cause is a second pass.

## Report

The findings and their mechanisms, the shared cause or the finding that there is none, the one change and exactly which findings it removes, what it costs and what it leaves behind, and your recommendation.
