# Issue #454 compact-parser correctness blocker

Status: **KEEP LIVE**. The PHP recovery-leaf candidate restores locked-C
correctness. Compact admission and performance work remain open. Keep issue
[#454](https://github.com/odvcencio/gotreesitter/issues/454) open.

## Scope

The original blocker record used base commit
`30f470f5c2bf18540f7a18b2b22a7e33b88d4e10`.
This rejection receipt uses base commit
`7498a678c52029a82f312e9637ecb66b15defa0b`.

The issue #454 report describes C latency and marks correctness as OK. This
repository uses an internal deterministic C witness. The witness uses a
repeated source near 137 kibibytes (KiB). The edit removes the first `x` from
`x0`. The transient malformed text becomes `0`. The original source has
140,288 bytes. The edited source has 140,287 bytes.

The smallest locked-C witness uses the same source prefix at 1,024 bytes. The
known-divergence ratchet is
`cgo_harness/issue454_c_compact_blocker_parity_test.go`.

## 2026-08-23 candidate rejection

This receipt rejects parser candidate pull request (PR) #793. The candidate
code and test patch hash is
`71fdb2ab00f8f31e74b7e165f381c0856bd3720abdeb4d1556454d0cc75c50fa`.
The rejected parser diff hash is
`60a2252eca3f65cf427d709c96729eb41572f2388b1fad64c946d390c3a94db1`.
PR #793 is closed without merge. No candidate parser code ships. Keep issue
#454 open.

### Continuous integration (CI) event matrix

Continuous integration (CI) run `32609724840` is red. The event matrix records
the required outcomes:

| Event | Result |
| --- | --- |
| Build | Red. |
| `race_packages (parser-result)` | Red. |
| `compact-t3-oracle-cgo` | Green. |
| `parity-cgo` and `parity-cgo-exhaustive` | Green. |
| `perf-regression` | Green. |
| `race_root` and `race_root_shards (0..4)` | Green. |
| PR #793 | Closed without merge. |

The failed build and parser-result race expose deterministic non-C changes.
The green parity and performance jobs do not prove grammar-agnostic safety.

### Variant matrix

The candidate has two independent changes. The matrix isolates each change:

- Remove `leaf.setHasError(true)` from `cAbsorbTokenIntoError`.
- Add a field-preserving hidden splice and scratch materialization in
  `cRecoverToState`.

A0 means the initial dispatcher census for a witness set.

| Variant | Parser-result tests | Cobol A0 rewrites | WGSL A0 rewrites | Cooklang raw result |
| --- | --- | ---: | ---: | --- |
| Base | Pass | 1,315 | 171 | Baseline digests |
| Leaf removal only | Fail | 8 | 28 | Changed digests |
| Field splice only | Pass | 1,315 | 171 | Baseline digests |
| Both changes | Fail | 8 | 28 | Changed digests |

The leaf-only parser diff hash is
`d044fa119454d5f3faf0d61a2e7317757c8aee489c4118e49dff0e4b1d751c8a`.
The field-only parser diff hash is
`e3816dac0c09c9d44ce11b918824db52c8fd2d2daf7a20c1a262a64e7440075f`.
The both-changes hash equals the rejected parser diff hash.

The leaf removal changes Cobol A0 by minus 1,307 entries. It changes WGSL A0
by minus 143 entries. The field splice does not change either census.

Cooklang raw digests show the same broad leaf effect:

| Witness | Base and field splice | Leaf removal and both | Locked C |
| --- | --- | --- | --- |
| Punctuation | `0e6880ec4902576c2a6de014424c3cba7eef99cdc5fd8fded8ceb6382a6df9cd` | `cab8c8e6c3d19cd86bbed655fe0c9a17720997d0248381464cca2b72f3fa868a` | `c6e4535b725516550ca7a0ee4c69974799c2d2d10fed4e5f1ba6b71e43c5ba8a` |
| Recovered | `896d9f79d941c3869dca7b855bae45738392d02519f0fbe3ac45cc2623fcfa2f` | `25665d13cdbac2ffa581c12e69d3407653f8d6a312fd87a675a9a21c706d1699` | `3ae5ffba70cd0922976d24ed3e4d254cbb9d356639e8485b8b4b3abdc2667133` |
| No newline | `f49ca1a85a0b2ee7ed7f07993d6bc8b103d66311f83d4a026e085cf2013a69ec` | `ba559b1d32c08534e063112eb327c18bd1370faf2b2d671e4b323f17ca5b7665` | `dd3692a1a0e9145af9f2d082126a1e798d60cbe74942427746d3f0e83bd31e1c` |

### Locked-C result

Direct locked-C deep digests confirm mixed support for the leaf change:

| Cobol size | Locked C | Base and field splice | Leaf removal and both |
| --- | --- | --- | --- |
| Small | `3461f2f548300b21ca8053ef6c804d63b029650c647ca6ba5092941cb0ac5e7a` | `d89f4a4c9004ec36fae3f8dfdc0cdd32c6e3b61d29f2d2f865ad5c42e677b3ee` (2 differences) | Exact C digest |
| Medium | `fb498ca27798ceca7acc9d006e7170944ec30a9248b3cc7a42bac2f0659f89a8` | `261aba9689d6ac0884980664335fdd645166ab642e6e8a1822afcca62a9c64f4` (125 differences) | `7887f2f30d2b54279d03ceb7fa8426d2743de0a4344a1f6f64aaec36f6155327` (9 differences) |
| Large | `48bc99e6f2c823674fc0cb7ef2306653d6db526ed502a4bc79f4d137a8f76380` | `9677691dc7381aa2f4118901c546a5755acb89fed1c744bdb5ea8b5cfd63dfe5` (1,185 differences) | Exact C digest |

The changed leaf behavior does not produce stable Cobol parity. It also
changes WGSL without reaching locked-C parity:

| WGSL witness | Locked C | Base and field splice | Leaf removal and both |
| --- | --- | --- | --- |
| Small | `d3e58954c750ed560edd3177a165bbf701c159467a1b4677996bec620c377804` | Exact C digest | Exact C digest |
| Normal | `231e10ca2215945a5fb51670620c9f5ba2ea1ca7d445cb2c9443fb51b8e0e18a` | `77fdfd002d6937e6f5784fc19e21a6f63ab8f2280ca8ba0dcfc1ee5b1d3d42cc` (1 difference) | `9d802a0e9af71176c0520496ae99425406aa76dc8560f8c7e939e366f9fbbd44` (1 difference) |
| Radiosity | `22b9d004c33c6a8229b56876282125e04efddf59deef6224eddd61f38c9952b2` | `c591b9329ad2fc946b6b8b7c4bc80adb7305f41934dd51d4505e4f606787b127` (32 differences) | `237cb89fafcc83e8c8cbe77786694caea4ba97fa86f2fab91e8d8db720429770` (12 differences) |

The field splice alone passes the parser-result tests. It leaves the known
1 KiB divergence unchanged. It does not fix issue #454 by itself.

Scoped Canopy traces the producer path as follows:

- `cRecoverStrategy1Election` calls `cRecoverToState`.
- `cRecoverToState` calls `cAppendVisibleSpliceWithFields`.
- `cRecover` and `cRecoverDispatchInError` call `cAbsorbTokenIntoError`.

The variant receipts are under
`/tmp/gts-issue454-variant-results/`. The C deep-digest receipts use the
`*-cobol-deep`, `*-wgsl-deep`, and `*-cooklang-raw-deep` directories.
The 1 KiB ratchet receipts use the `*-issue454-1k-ratchet` directories.

The exploratory C-name guard has diff hash
`8143175e94fcbc4073cf36fa4ea8e51f7f276fec4ac0f69d829dca7b91188027`.
It passes the five-size C probe and the incremental probes. Reject it because it selects a
grammar name instead of a grammar-agnostic semantic predicate.

### Controlled memory result

The controlled real-corpus audit used three alternating base and candidate pairs.
The logs are under `/tmp/gts-issue454-rss-audit/`.
The paired diagnostic logs are:

- `/tmp/gts-issue454-rss-audit/rep1-base/20260823T004613Z/real_corpus/diag_c_lang.log` and `/tmp/gts-issue454-rss-audit/rep1-candidate/20260823T004629Z/real_corpus/diag_c_lang.log`.
- `/tmp/gts-issue454-rss-audit/rep2-base/20260823T004702Z/real_corpus/diag_c_lang.log` and `/tmp/gts-issue454-rss-audit/rep2-candidate/20260823T004646Z/real_corpus/diag_c_lang.log`.
- `/tmp/gts-issue454-rss-audit/rep3-base/20260823T004719Z/real_corpus/diag_c_lang.log` and `/tmp/gts-issue454-rss-audit/rep3-candidate/20260823T004917Z/real_corpus/diag_c_lang.log`.

Resident set size (RSS) is the process memory held in RAM.

| Pair | Base RSS | Candidate RSS | Base elapsed | Candidate elapsed |
| --- | ---: | ---: | ---: | ---: |
| 1 | 566,680 KiB | 609,464 KiB | 10.03 s | 10.36 s |
| 2 | 628,852 KiB | 606,980 KiB | 10.48 s | 10.07 s |
| 3 | 593,812 KiB | 620,544 KiB | 10.02 s | 10.03 s |
| Mean | 596,448 KiB | 612,329 KiB (+2.66%) | 10.18 s | 10.15 s |

The earlier candidate RSS result of 1,089,364 KiB did not recur. The focused
incremental workload measured 857,624 KiB for base and 842,900 KiB for the
candidate. Memory results do not remove the correctness rejection.

## Reopening conditions

Reopen this work only when all conditions pass:

1. Keep the 1 KiB known-divergence ratchet as the current CI guard.
2. Derive a new grammar-agnostic semantic predicate for the leaf error flag.
3. Clear that flag only when the predicate proves the C recovery semantics.
4. Preserve all unrelated grammar census and digest results.
5. Match locked C at 1, 4, 16, 64, and 137 KiB for fresh and incremental trees.
6. Pass the replace, insert, delete, parser-result, and C recovery gates.
7. Preserve the memory-budget fallback as a separate correctness concern.

## Locked-C evidence

The guard compares a fresh Go tree with the pinned C parser. Both roots report
an error. The first deep-tree difference is:

```text
/translation_unit/function_definition[0]/compound_statement[2]/ERROR[2]/number_literal[0]
category=error Go=true C=false
```

The fresh Go tree differs from locked C at every tested size:

| Source size | Result |
| --- | --- |
| 1 KiB (1,024 bytes) | The first difference is the recorded `number_literal` error flag. |
| 4 KiB (4,096 bytes) | The same first difference remains. |
| 16 KiB (16,384 bytes) | The same first difference remains. |
| 64 KiB (65,536 bytes) | The same first difference remains. |
| 137 KiB (140,288 bytes) | The same first difference remains. |

The structure probe records all digest pairs in
`/tmp/gts-issue454-artifacts/20260822T221718Z-issue454-c-structure/container.log`.

## Incremental evidence

The 137 KiB incremental Go tree equals the fresh Go tree. Both have digest
`9c979bb436f92e7f96885454de81d9d95d2befff242145b1026addf5d9395c4d`.

The locked-C digest is
`8fe04819317a4f225b5c298a71acdceb5aa965abb3a397e35eb48c5849888d5c`.

The incremental profile reports
`ReuseUnsupportedReason=incremental_parse_memory_budget_full_retry`.
The parser completes the edit after the memory-budget fallback. The existing
replace, insert, and delete regression passes in
`/tmp/gts-issue454-artifacts/20260822T221438Z-issue454-c-current`.

The fresh Go mismatch proves that retry selection cannot repair the C parity
difference. The fallback fixes the incremental failure mode only.

## Ownership and decision

The first difference is a child error flag inside a recovered declaration. It
appears during fresh parsing, before incremental retry selection. The likely
owner is generic recovery or error-node materialization. A bounded generic fix
is not safe without wider recovery validation.

The 1 KiB known-divergence ratchet passes in
`/tmp/gts-issue454-artifacts-rebase/20260822T230037Z-issue454-c-1k-ratchet-20260822`.

## 2026-08-24 PHP recovery-leaf correction

Candidate base: `55681868d3a23971d042f9f79083fd6d39c7e33b`.

Status: **CORRECTNESS FIX / KEEP LIVE**. Keep issue #454 open.

The correction adds positive internal deterministic finite automaton (DFA)
provenance to tokens. Recovery requires a source-bearing parsed stack prefix.
It then records a region proof only for a direct, visible, named DFA token.
The prefix proof rejects pending, missing, error, dirty, and invalid payloads.
It rejects state mismatches and payloads that end after the current token starts.
It rejects the end-of-input symbol and zero-width tokens. It also rejects
generated, external, missing, no-lookahead, and error-mode tokens.

Each later absorbed token must carry its own positive DFA proof. A region
proof cannot clear a later token by itself. A skipped-prefix token can qualify
only when the internal DFA produced that token directly.

The edited PHP source has 140,287 bytes. Its SHA-256 is
`cbf52f81ea212353a3bf04d7c9b37668b5cdfb6cd428c2d0cb3799a8e13ae82f`.
The PHP grammar uses commit `3f2465c217d0a966d41e584b42d75522f2a3149e`.
The embedded PHP grammar blob SHA-256 is
`15724627db479c27304b43fa3b5ef7d8d81f85e3b9ce6d8575a847b2dbaa5cd5`.
The locked-C artifact SHA-256 is
`1daea60ac1ee31227b8e1ed3cbd76b841435fe693e95af65cc61dad447d27891`.

The `gts-deep-tree-v1` stream covers type and named identity, incoming fields,
byte and point spans, and child order. It also covers extra and missing flags,
error flags, and the `HasError` flag.

The pinned deep digests are:

| Route | Deep digest | Root `HasError` |
| --- | --- | --- |
| Raw Go | `1516308c38163089778464ad171875308c559af11af7c8c03ee17ae4eacd23c6` | `true` |
| Production Go | `1516308c38163089778464ad171875308c559af11af7c8c03ee17ae4eacd23c6` | `true` |
| Compact fallback Go | `1516308c38163089778464ad171875308c559af11af7c8c03ee17ae4eacd23c6` | `true` |
| Locked C | `1516308c38163089778464ad171875308c559af11af7c8c03ee17ae4eacd23c6` | `true` |

The raw, production, compact fallback, and locked-C deep digests are equal.
All four roots report `HasError=true`.

The compact route recorded `routed=0` and `fallback=1`. Its fallback reason
still reports `mechanism=recovery-entered`. The compact parser does not accept
this recovery path yet.

This correction does not graduate PHP compact admission. It does not close
the remaining issue #454 performance work. The incremental memory-budget
fallback and the large-file resident-set-size target also remain open.
