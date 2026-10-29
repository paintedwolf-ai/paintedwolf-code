# Scoped trust and interception

## Inside this sandbox

The in-app proxy tunnels TLS bytes without replacing the origin certificate. An independent corporate proxy may inspect TLS. Check the presented chain and structured network observations before assigning a cause; use `reach-a-network-service` for routing failures.

## Trust one CA for one client

Scope trust to the call that needs it, as an environment variable or client flag on that `command`, never machine-wide:

| Client | Per-call setting |
|---|---|
| Node.js | `NODE_EXTRA_CA_CERTS=<bundle.pem>` |
| Python `requests` | `REQUESTS_CA_BUNDLE=<bundle.pem>` |
| OpenSSL-based clients, Python `ssl`, Ruby | `SSL_CERT_FILE=<bundle.pem>` |
| Git over HTTPS | `GIT_SSL_CAINFO=<bundle.pem>` or `git -c http.sslCAInfo=<bundle.pem>` |
| `openssl s_client` | `-CAfile <bundle.pem>` |

Use the bundle the organization provides for a TLS-inspecting proxy, or the internal CA's own certificate. Keep the bundle outside the repository unless the project already ships it. Report that interception is happening when the presented issuer is a proxy or security vendor.

Never combine a scoped bundle with a verification-disabling flag, and never add the bundle to the system trust store.
