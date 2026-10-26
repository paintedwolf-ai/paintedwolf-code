# Managed references

## Where references resolve

`secret_generate`, a `response_type: secret` answer from `ask_user`, and a Protected screen detection each return a managed reference. The host resolves references in execution copies of outbound process and network calls (`command` programs, including native `curl`; `verify`; `terminal_open`; `terminal_send`; MCP tools) and in file mutations (redirects and `tee` inside `command`, `edit`, `replace_lines`, `jq_edit`). The canonical call, transcript, approval, and receipt keep the reference.

A reference may be a whole value or part of one, for example `Bearer <ref>` in a header or `postgres://app:<ref>@127.0.0.1:5432/app` in a connection string.

## Writing configuration files

For a file write, the host writes the value when the file is saved; the approval posture decides whether the person is asked first. Once written, the host tracks and highlights the secret span, and later reads of the file show the reference instead of the value. Use project scope for durable project files so a later chat can still resolve them.

A reference can also appear where the host found a managed value in a read or other output. The source still holds the real value: pass the reference unchanged when writing configuration or running a process.

## Repeated service use

When a `command` or terminal call sets up an authenticated service, add `secret_use` with the intended origins, e.g. `{"services": ["http://127.0.0.1:8080"]}`. Each origin is scheme, host, and port only. Keep the reference in the consumer arguments and request the setup's normal capabilities; the host reviews process access, the protected value, and the named connections together. Later calls to that origin reuse the permission.

This is explicit permission, not evidence that a daemon cannot retain or forward the value. A changed value or recipient may require review again.

## Lifecycle checks

Use `secret_list` when lifecycle state is part of the task: establish the precondition, confirm new metadata after `secret_generate`, and confirm revoked or expired state after `secret_revoke`. Revocation permanently disables resolution; protected screening evidence remains until project deletion. Listing names reveals architecture, so enumerate only when the task requires it.
