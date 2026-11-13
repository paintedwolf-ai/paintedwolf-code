# Supabase CLI and RLS

## CLI commands

- `supabase projects list` and `supabase status` show the linked project and the local stack. The link is stored in the repo and persists between sessions.
- `supabase start` runs the local stack on Docker; if it will not come up, the container skills apply. `supabase status` prints the local URLs and keys.
- `supabase migration new <name>` creates a timestamped migration file; put the DDL there.
- `supabase db diff` generates a migration from changes you made in Studio — prefer it to hand-writing them from memory, because a diff can pick up more than you intended; read the generated SQL before committing it.
- `supabase db reset` drops the local database and replays every migration from the start. That is the only way to know the migration chain applies cleanly to an empty database, which is what a fresh environment does.
- `supabase db push` applies migrations to the linked remote project.

## Row level security

Publishable and legacy `anon` keys are intentionally public; Postgres grants plus RLS constrain what unauthenticated and authenticated roles can do. Enable RLS and least-privilege policies on every table exposed through the Data API, and put a new table's RLS state and policies in the same migration that creates it.

## Keys and what they mean

- The current **publishable key** (`sb_publishable_…`) is designed for public clients. The legacy `anon` JWT key is its older equivalent and is scheduled for deprecation; do not introduce the legacy key into new code when the project has migrated.
- The current **secret key** (`sb_secret_…`) and legacy `service_role` key are backend-only credentials that authorize elevated access and bypass RLS. Never put either in client code, a committed file, or the transcript. If one appears in a public surface, report potential exposure rather than using it.
- Local keys printed by `supabase status` are fixed development values, but the outbound screen flags JWT-shaped keys: do not print them to read them, and Protect them when flagged. Remote keys are managed references only.
