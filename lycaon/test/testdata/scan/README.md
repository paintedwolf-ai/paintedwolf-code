# Scan E2E fixtures

Minimal project tree used by bundled scanner E2E:

- `api_scan_bundled_findings_e2e_test.go` — single-scanner `POST /v1/scans/full`
- `api_scan_pack_findings_e2e_test.go` — coordinator `scan_pack` fan-out + aggregate
- `scan_pack_enqueue_contract_test.go` — enqueue fan-out (no binaries)

| File | Scanner | Expected signal |
|------|---------|-----------------|
| `secrets.env` | gitleaks (`secret`) | GitHub token–shaped secret |
| `package-lock.json` | osv-scalibr (`sca`) | Known-vuln `lodash@4.17.4` (unit tests match a vendored OSV record) |
| `vuln.go` | OpenGrep (`sast`) | TLS verification bypass in `vuln.go` (skipped under `-short`) |

Do not run as an application; static analysis only.
