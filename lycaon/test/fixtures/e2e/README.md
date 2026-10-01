# E2E fixtures

Static trees copied into temp project dirs for Den Playwright and Go security journeys.

| Path | Purpose |
|------|---------|
| `minimal-go-project/` | Tiny module for implement-mode / scan drawer scenarios |
| `hostile-project/` | Encodings, symlink, nested `.paintedwolf/`, binary, detection-bait script — for harness hunters (`scripts/harness/seed-hostile-project.sh`) |
| `docker-compose.e2e.yml` | Tier B sidecar + Vite stack (used by `./task e2e:den` when Docker is available) |

Teardown: `./task e2e:den` removes temp state, Playwright output, and compose volumes (`down -v`). After an interrupted run, `./task e2e:cleanup`.

Go API E2E uses `lycaon/test/wiring/fixtures` and `t.TempDir()` — not this directory.

## Usage (Den)

```bash
# From repo root — Docker stack (random project/ports); Docker daemon required
./task e2e:den

# Filter by title
LYCAON_E2E_GREP=boot ./task e2e:den
```

Copy `minimal-go-project` into a session `project_dir` when a scenario needs a real tree on disk (see phase plans for scans drawer).
