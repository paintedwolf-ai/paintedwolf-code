# Selective handoff invalidation

Record R17's original handoffs, then reconcile amendment.json before sealing the release.

baseline records the original revision and artifact for every region in handoffs.json; amendments do not rewrite that record.
In reconcile, choose correct to append a correction or seal to publish the final index. Apply the amendment and renew every handoff whose dependency binding no longer matches the resulting revisions. Select a passed replacement for the requested release with matching dependencies from replacements.json. Repeat until every binding matches. Leave unaffected handoffs unchanged.

correction records region, before revision, revision, before artifact, artifact and dependency revision (none for no dependency). Record each changed region once, prerequisites before dependents. release index records each region's final revision and artifact.

All supplied files are immutable. Deliver records through this workflow; do not create files. Cite the source records used for every terminal verdict.
