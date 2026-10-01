Define and explain the code at {{ path }} lines {{ start_line }}–{{ end_line }} in its repository context.

Call `summarize` on that path with a task focused on this range, then use file survey tools when needed to ground important relationships. Reply with a concise semantic definition organized around:

1. What it is.
2. The role it plays here.
3. Important inputs, outputs, dependencies, and side effects.
4. Guarantees, invariants, and failure behavior that matter.
5. Important callers, consumers, tests, or documentation, with file references.
6. What a maintainer should be careful about.

Distinguish repository evidence from inference. Omit sections that do not apply. Do not merely paraphrase syntax. Do not edit files. Do not run shell or network tools.
