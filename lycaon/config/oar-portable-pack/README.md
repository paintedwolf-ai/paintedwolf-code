# Portable Open Agent Rules pack

Rules that `LintPortability` reports portable: core anchors and core/standard
facts only. They are the extract a second host can load without this engine's
`lycaon.*` observations.

Source siblings live in the standard's
[`examples/portable-pack/`](https://github.com/paintedwolf-ai/open-agent-rules/tree/main/examples/portable-pack).
`capability.yaml` and `config.yaml` are host/operator documents, not rules.

CI: `TestPortablePackIsPortable` in `internal/oar`.
