## Path scopes (plan workflow)

Governing blueprint paths — `${overlay_dir}/blueprints/**`.

- **Read paths:** `{{ read_globs|join:"`, `" }}`
- **Write paths:** `{{ write_globs|join:"`, `" }}`

Plan phases gate which tools are callable — follow the mode instructions for the current phase.
