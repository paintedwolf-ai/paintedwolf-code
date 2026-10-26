# Managed references

## Where references resolve

The host resolves references in execution copies for outbound process and network tools (`command`, `verify`, `terminal_open`, `terminal_send`, `http_request`, and MCP tools), and in file mutation tools (`write`, `edit`, `replace_lines`, `jq_edit`) when configuring files. The canonical call, transcript, approval action, and invocation receipt retain the reference. For file writes, the host writes the value when the file is saved; your approval posture decides whether you're asked first. Once written, the host tracks and highlights the secret span, and reads of the file project dynamic references. Use project scope for durable project files.

A reference can also appear where the host found a managed value in a read or other tool output. The source still holds the real value: pass the reference unchanged when writing to configuration files or executing processes.

## Repeated service use

When a command or terminal call sets up an authenticated service, include `secret_use: {"services": ["http://127.0.0.1:8080"]}` with the actual intended origins. Each origin contains only scheme, host, and port. Keep the managed reference in the consumer arguments and request the setup's normal capabilities. The host can review process access, protected-value use, and the named local service connections together. Later `http_request` calls use that reference and origin. This is explicit permission, not evidence that a daemon or process cannot retain or forward the value. A changed value or recipient may require review again.
