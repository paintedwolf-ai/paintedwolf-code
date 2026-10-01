# Measurement patterns

| Situation | Keep this state | Probe | Evidence to report |
|---|---|---|---|
| A disclosure should appear after activation | `page_open` → `page_act` → live `id` | The trigger and disclosed panel | Panel `rendered`, panel/trigger gap, and overlap if relevant |
| A modal or popover might cover an action | Open it with `page_act` and retain the live `id` | Dialog, primary action, and the control behind it | The action's `reach` (`covered_by`) plus the overlap relation |
| Cards or toolbar controls should line up | Initial state or live `id`, whichever is being reviewed | The compared card or control selectors | Alignment relations and widths/heights |
| A narrow viewport clips a target | `page_open` with `viewport: {width, height}`, then retain the live `id` (re-opening a held target reloads it and loses driven state) | Target and its enclosing region | `reach` of `outside_viewport` or `clipped`, rect, and edge distances |
| Text needs a numeric color check | The actual state/background under review | The text-bearing element | Contrast relation when the rendered foreground and background are supported; otherwise report contrast unavailable |

Do not use a broad selector such as `button` or `div`. Start from structural evidence, then select the one control or region that answers the question.
