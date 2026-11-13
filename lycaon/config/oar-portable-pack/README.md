# Portable Open Agent Rules pack

The Open Agent Rules 1.0 example pack, carried unchanged from the standard's
[`examples/portable-pack/`](https://github.com/paintedwolf-ai/open-agent-rules/tree/main/examples/portable-pack).
Every rule uses core anchors and core or standard-profile facts, so it loads on
any conforming engine without Painted Wolf Code's `paintedwolf.*` facts.

`profile.yaml` is the anchor profile map of the host the rules assume.
`fixtures/` replays each behaviour.

CI: `TestPortablePackIsPortable` loads every rule through the production engine,
checks each is portable, and replays every fixture.
