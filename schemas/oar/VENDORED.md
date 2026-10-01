# OAR source pin

The schemas, vocabulary, license, and complete conformance corpus in this
folder are copied from the OAR 1.0 source bundle. `release-manifest.json` pins
that bundle by its content hash. `vendor-manifest.json` maps each local artifact
to its source path and SHA-256 digest. This document is local documentation.

The OAR standard, reference evaluator, and host implementations are being
developed together before v1. Change normative sources in the
`open-agent-rules` repository, then run `./task oar:vendor` here. The production
Go pipeline runs every vendored fixture in
`internal/oar/TestProductionEngineAgainstPublishedCorpus`.

`./task oar:vendor:check` verifies the pin, file membership, and bytes without
network access. When the standard checkout is available, it also checks that
the local pin matches that source bundle. Set `OAR_STANDARD_DIR` to its root or
`OAR_SPEC_DIR` to a version directory to select a different checkout. A released
version is immutable; refresh a draft in place only before its publication.
