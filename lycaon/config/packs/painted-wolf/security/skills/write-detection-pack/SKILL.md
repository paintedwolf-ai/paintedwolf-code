---
name: write-detection-pack
description: Create and test additive Sigma detections that raise approval asks or egress holds on tool and network events.
---

# Write a detection pack

1. Confirm the external-system risk and expected approval escalation. A detection pack only adds an ask or egress hold; it never proves a command safe.
2. Pick where the pack lives, because it decides the loop and nothing else:
   - **In this repository** — `lycaon/config/packs/painted-wolf/security/host/detection-packs/<pack_id>/`, shipped with the app.
   - **In an extension pack** — `host/detection-packs/<pack_id>/` in that pack's tree, for rules more than one machine should have. This is the answer whenever a team, not a person, wants the rules.
   - **A folder on this device** — anywhere on disk, imported through Settings, for a rule you are writing right now.
3. Create the pack folder with `pack.yaml`, one or more `rules/*.yml`, and `fixtures.yaml`. The manifest `id` must equal the directory name, and every rule takes a fresh UUID. The layout is identical in all three places.
4. Write rules only against the supported synthetic tool-exec and egress-observed fields and supported Sigma subset. Include at least one positive and one negative fixture for each rule. Negatives are checked against every rule in the pack, so put the rehearsal forms of the operation (`--dry-run`, `plan`, `--help`) there.
5. Validate by destination:
   - In-repository: `./task test:digest -- ./internal/detectionpack/...`.
   - Extension pack: `pw extensions validate` — it compiles the graph and rehearses every fixture case through the production adapters.
   - Device folder: Settings → Approvals → **Detections** → **Add pack…** as a dry-run preview; read every inactive, unsupported, ignored, and rehearsal line. The repository digest does not validate that folder.
6. Report what the rules will and will not do. A pack id already provided by another pack is refused rather than merged, a pack may never contribute a rule into another pack's directory, and nothing here can replace or remove a rule somebody else wrote — additive only, at every scope.
7. Stop and request a product design when the desired behavior needs automatic allow, an expanded containment floor, a new synthetic event field, cloud action lists in Go, prose classification, or any way for one pack to weaken another's rule.

See [Sigma subset](references/sigma-subset.md) for accepted keys, logsource shapes, modifiers, severity/posture behavior, and the authoring loops. A miss is not evidence of safety.
