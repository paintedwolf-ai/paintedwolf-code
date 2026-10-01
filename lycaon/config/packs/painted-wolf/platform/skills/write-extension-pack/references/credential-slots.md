# Credential-slot recognition

Add a flat `host/credential-slots/<name>.yaml` file to a device extension pack.
Declare `host.credential_slots` in the manifest's
`compatibility.requires_capabilities`. The unit id is
`host/credential-slots/<pack-id>:<name>`. Units compose additively and cannot use
`own`; a project cannot contribute or disable them.

```yaml
version: 1
surfaces:
  service_apply: {kind: content, content_arg: document}
env_keys: [SERVICE_ACCESS_VALUE]
file_keys: [SERVICE_ACCESS_VALUE]
value_flags: [--access-value]
key_terms: [passcode]
key_term_pairs: [[login, material]]
command_images:
  - image: servicectl
    require_flags: [--create]
    value_flags: [-x, --access-value]
```

Only `version` is required; supply at least one recognition entry. Documents
are limited to 64 KiB and 256 entries. Each command's flag lists are limited to
256 items. Names are at most 128 bytes and contain letters, digits, underscores,
dots or hyphens. Unknown fields, duplicate YAML keys and multiple documents are
errors. Wildcards and regular expressions are unsupported.

- `env_keys` adds case-insensitive exact environment and nested structured keys.
- `file_keys` adds case-insensitive exact keys in authored configuration.
- `value_flags` accepts long flags with separate or `=`-attached values.
- `key_terms` contains lowercase single segments, matched at the end of a key.
- `key_term_pairs` contains pairs of lowercase segments, in order at the end.
- `command_images` selects an executable basename. Optional `require_flags`
  restricts the match; supply `value_flags`, `value: last_positional`, or both.
  Flags match case exactly and may be long or one-character short names. Glued short values are unsupported.
- `surfaces` maps exact tool names to host parsers. `content` and `terminal`
  require a top-level string `content_arg`. `command` uses the standard command
  and stages arguments and forbids `content_arg`. Independent units may add
  multiple payload arguments for one tool without replacing existing mappings.

Definitions cannot change exclusions, SQL parsing, password lists, inspection
limits or managed-reference handling. Matching produces a possible credential
observation; it neither establishes a secret nor proves unrecognized values
safe. OAR rules own warning conditions, counters and copy.

Validate, install at device scope, and inspect effective units and diagnostics.
After editing a linked pack, reload it. Recognition follows the current catalog
view at the next eligible tool observation. Disabling or removing a unit removes
its additions while preserving the bundled baseline.
