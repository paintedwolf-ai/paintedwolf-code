## Path scopes (spec posture)

Read-heavy coordinator path policy for specify-mode chat.

- **Read paths:** `{{ read_globs|join:"`, `" }}`
- **Write paths:** `{{ write_globs|join:"`, `" }}`

Spec posture blocks delegation and state tools until plan workflow gates allow — branch on `SPEC_POSTURE_*` codes.
