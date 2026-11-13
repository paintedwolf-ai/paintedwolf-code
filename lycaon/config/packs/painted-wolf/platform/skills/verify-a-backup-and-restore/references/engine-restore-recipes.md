# Engine restore recipes

## The recovery claim

Write the claim before touching anything — it is what the rehearsal tests against:

```text
system:   orders-db (postgres 16.3)
backup:   full 2026-08-11T02:00Z + WAL through 09:15Z
RPO:      ≤ 15 min      RTO: ≤ 30 min
needs:    base backup, WAL archive, KMS key alias orders-backup
```

Record whether the restore needs a full backup plus incrementals, logs, manifests, or encryption keys.

## Inspecting the backup

Use the tool's list, manifest, checksum, or verify mode without modifying the source artifact, and confirm every required segment is present. Keep the original read-only.

## Isolating the target

Keep outbound notifications, scheduled jobs, webhooks, and replication from starting, and do not inject credentials that can reach an external system.

## The restore run

Use the same automation and key-retrieval procedure an incident would use, restore the complete chain to the chosen point, fail on errors, and measure elapsed time.
