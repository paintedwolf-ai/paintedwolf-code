Document the code or prose at {{ path }} lines {{ start_line }}–{{ end_line }}.

Use file survey tools to understand the selected range and nearby conventions, then add or update the smallest useful documentation in this same file. For code, prefer an idiomatic doc comment attached to the selected declaration. For prose, schemas, or configuration, improve the selected documentation or field descriptions in place.

Capture purpose, contract, important invariants, and non-obvious failure behavior when they are supported by repository evidence. Preserve behavior. Do not narrate obvious syntax, invent guarantees, create a new file, or change unrelated content. Use file edit tools only; the host rejects writes outside this file. When the documentation edit is written, stop.
