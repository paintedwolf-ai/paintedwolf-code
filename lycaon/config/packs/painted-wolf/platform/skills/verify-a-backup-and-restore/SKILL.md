---
name: verify-a-backup-and-restore
description: Prove a backup restores required data into an isolated target within the recovery-time objective.
---

# Verify a backup and restore

**Entry check:** you have an isolated target to restore into and permission to use it. Without one, stop — there is no safe way to run this, and a validator command against the backup file is not a substitute.

## Workflow

1. **Write the recovery claim before touching anything.** This is what the rehearsal tests against:

   ```text
   system:   orders-db (postgres 16.3)
   backup:   full 2026-08-11T02:00Z + WAL through 09:15Z
   RPO:      ≤ 15 min      RTO: ≤ 30 min
   needs:    base backup, WAL archive, KMS key alias orders-backup
   ```

   Record whether the restore needs a full backup plus incrementals, logs, manifests, or encryption keys.
2. **Inspect before restoring.** Use the tool's list, manifest, checksum, or verify mode without modifying the source artifact, and confirm every required segment is present. Keep the original read-only. A validator success is evidence the bytes survived, not that the system restores.
3. **Create the isolated target.** A new empty database, disposable instance, or recovery environment with no route back to production. Keep outbound notifications, scheduled jobs, webhooks, and replication from starting, and do not inject credentials that can reach an external system. Do not alter shared credential state; generate the target's credentials with `secret_generate` (chat scope) and revoke them at cleanup. State the target explicitly before loading data.
4. **Restore through the documented path and start the clock.** Use the same automation and key-retrieval procedure an incident would use, restore the complete chain to the chosen point, fail on errors, and measure elapsed time. Do not hand-repair the backup during the timed run — record any intervention as a failed or conditional step, because in an incident nobody will know to make it.
5. **Verify usability at four layers**, in order: engine integrity check → schema and object counts against expectation → representative row counts and invariants → bounded read-only application smoke queries. Check permissions and required extensions separately. "The server started" is not evidence the data is correct.
6. **Compare the result with the claim** from step 1: recovered timestamp, data gap, restore duration, validation results, missing dependencies, and whether measured RPO and RTO were met.
7. **Hand off cleanup deliberately.** Preserve the restored target when a human needs to inspect it. Remove only task-created resources, and only after confirming they are not the sole evidence.

## Stopping rule

The rehearsal ends when all four verification layers pass and measured RPO and RTO are compared against the claim — pass or fail. A restore that succeeded but missed RTO is a **failed rehearsal with a working backup**, and reporting it as success is the outcome this skill exists to prevent.

## Boundaries

- Never restore over an existing production or shared database, volume, bucket, or account.
- A successful backup job, object-store checksum, archive listing, or vendor `validate` does not prove restorability.
- Do not print backup contents, encryption keys, connection strings, or recovered personal data into the transcript.
- Do not weaken integrity checks, skip failed objects, or substitute a newer backup to make the rehearsal pass.
- Point-in-time recovery must name the exact time or log position used; "latest" is not a reproducible recovery point.

## Report

The recovery claim, the isolated target, the restore command path and elapsed time, the four verification layers with results, measured against required RPO and RTO, every manual intervention, and what was cleaned up or preserved.
