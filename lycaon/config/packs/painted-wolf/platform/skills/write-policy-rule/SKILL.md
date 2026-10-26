---
name: write-policy-rule
description: Author and test Open Agent Rules over supported machine-state anchors and facts without new host behavior.
# `copy` is the OAR presentation object, not the copy tool.
optional_tools:
  - copy
---

# Write a policy rule

1. State the observable machine condition, lifecycle anchor, intended effect, and expected positive and negative cases. Do not translate conversation prose into a host classifier.
2. Confirm the work is a rule for the existing OAR format and host capability document. Put an extension rule directly under `policy/` as `<ID>.yaml`. Identity comes from the document's `namespace`/`id`, but keep the stem equal to `id` so a single-file `pw rules test` selects it; do not edit the standard, vendored schemas, or host fact catalogue as part of rule authoring.
3. Author `oar: "1.0"`, `id`, optional `namespace`, `kind`, `anchor`, any fact-named `selector`, declared `requires`, `when`, `effect`, and presentation `copy`. Document identity is independent of the filename. New rules may use any implemented anchor; transforms require a boundary that can deliver rewritten content. Prefer portable core anchors and facts unless the extension deliberately targets a documented host capability.
4. Add conformance fixtures under the pack's sibling `conformance/` directory for at least one firing and one non-firing observation. Keep verdict logic in selector and `when`; copy and `x-` presentation fields must not decide whether the rule fires.
5. Run `pw rules test <policy-path> --json`. Branch on `code` (`schema_invalid`, `condition_invalid`, `scenario_mismatch`, or `load_error`), fix the rule or fixture, and rerun before installing the pack.
6. Validate and link/install the containing extension pack, then inspect effective units and diagnostics. A passing rule test proves schema, compilation, and declared fixtures—not that every runtime case is covered.
7. Stop for product design when the behavior needs a new fact, anchor, detector, `on_fire` action, transform capability, standard-format change, or automatic permission grant.

See [rule contract](references/rule-contract.md) for the document shape, portability boundary, fixture placement, and test codes.
