# Direct IP and SOCKS

## Why SOCKS is opt-in

Default confinement injects the **HTTP** front door only. Co-injecting SOCKS into every process steers HTTP clients that honor `ALL_PROXY` onto an optional SOCKS stack they often lack — that failure is a client/proxy-scheme mismatch, not a down broker. Opt in with `socks_proxy` when the client actually needs SOCKS. That flag does not widen the sandbox.

## Requesting direct IP

1. **The other reason for direct IP is a client that ignores proxy variables** — not the protocol. If a tool refuses `HTTPS_PROXY` / `ALL_PROXY` even after `socks_proxy: true`, direct IP is the fallback even for TCP.
2. **When you request direct IP, declare your destinations.** `declared_destinations` takes `host:port` with an optional `tcp://` or `udp://` scheme. Declaring narrows the sandbox to those transports — `udp://time.nist.gov:123` permits UDP/123 and refuses everything else. Declaring nothing leaves every protocol and port open, which is a larger request for the same work. Do not combine `socks_proxy` with `direct_ip`.
3. **Get the format right or you lose the narrowing.** Parsing is all-or-nothing: an entry without a port, or a scheme like `https://` that only implies one, drops narrowing for the whole request.
4. **Resolve names normally under direct IP.** It is the one mode where the system resolver works, so you do not need to hand-resolve literal addresses.
