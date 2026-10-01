# Wire enum vocabulary (SSOT)

YAML files in this directory are the **only** write path for the closed OpenAPI
string enums they name. `lycaon/cmd/codegen-wire-enums` reads them; unknown
keys are an **error**.

## Authoring loop

1. Edit `docs/openapi/vocab/<EnumName>.yaml`: exact wire strings under
   `values` (per value: `id`, optional `go` const name, `comment` for the Go
   const doc, `deprecated` / `successor`), plus optional `description`,
   `go_const_prefix`, `go_file`, `json_schema`, and `http_status`.
2. Run `./task codegen:wire-enums`, then `./task openapi:bundle`, then
   `./task codegen:den-types`.
3. Every `EventTopic` value must declare its `payload` component `$ref`. The
   generator emits the OpenAPI discriminated envelope, the Den payload map and
   topic list, and one JSON Schema ref per topic; update
   `TOPIC_STORE_INVALIDATION` / `dispatchTopic` in `lycaon-den/src/api/events.ts`
   when adding a topic.
4. Confirm `./task codegen:wire-enums:check` stays green; the gate runs it.

Do **not** hand-edit `*.generated.yaml`, any `*.generated.go` under
`lycaon/pkg/api/` (the filename is whatever `go_file:` declares),
`event-topics.generated.ts`, `event-payloads.generated.ts`, generated event
schemas, or any `ts_labels` / `state_machine` output. Do **not** put hint codes
or agent ids here.

## HTTP status per value (`http_status`)

An enum that declares `http_status: true` gives every value exactly one
`status:` (400–599), and the generator emits `func (c <Enum>) HTTPStatus() int`.
`ApiErrorCode` is that enum: the Go responder derives the response status from
the code, so a code means one status everywhere. A `status:` on any other enum
is an error. `ApiErrorCode` values are lowercase snake_case, a 404 code is
`<resource>_not_found`, and a retired code is deleted rather than marked
`deprecated` (the contract tests enforce all three).

## State machines (`state_machine`)

An enum may declare its legal transitions, which the generator projects three
ways from one table. It requires `json_schema`, `sql_check.path`, and `ts_file`:

```yaml
json_schema: docs/schemas/events/session-status.generated.json
state_machine:
  ts_file: lycaon-den/src/api/session-status.generated.ts
  initial: [preparing, idle]
  transitions:
    preparing: [idle, error]
    idle: [busy, error]
  sql_check:
    path: lycaon/internal/db/schema.sql
```

| Output | Surface |
|--------|---------|
| `pkg/api` | `IsValid…`, `IsInitial…`, `CanTransition…`, for the store that writes the state |
| `sql_check.path` | the `status IN (…)` CHECK, rewritten between `-- codegen:<Enum>:start` / `:end` markers |
| `ts_file` | the transition table and `isEnterable…` |

The Den projection is narrower on purpose. The store validates creation and
every step, so the wire carries only legal sequences; what a client can still
get wrong is **order**: a snapshot fetched before an update can land after it.
A state nothing transitions into proves that happened. A step check would also
reject the legal case of a skipped intermediate state, so the client does not
get one.

## Den label tables (`ts_labels`)

An enum may declare a generated, exhaustive id → label table for Den.
`path`, `const`, and `type_import` are required:

```yaml
ts_labels:
  path: lycaon-den/src/chat/checkpoint/gate-copy.generated.ts
  const: GATE_LABELS
  type_import: ../../api/types.ts
  doc: One sentence on what these labels are for.
values:
  - id: user_rule
    comment: fires on an ask the user or the project authored.   # Go const doc
    label: A rule you wrote asks about this                      # Den card row
```

Two guards, both fail closed: the generator refuses a value with no `label` (and
a `label` on an enum with no `ts_labels`), and the emitted table ends in
`satisfies Record<EnumName, string>`, so a gap fails `den:typecheck`.

**Only for copy Den cannot get over the wire.** If the string already rides a
response (an `ApprovalOption` title, say), generating it here gives one string
two delivery channels, which is the drift this directory exists to prevent.
