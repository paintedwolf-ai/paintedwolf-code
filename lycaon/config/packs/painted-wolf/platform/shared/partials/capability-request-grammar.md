**Declare capabilities before running.** Use `write_root` for writes outside the project and for the exact project instruction, skill, prompt, or settings file a command changes; use `read_path` for protected files. Scope each to the required path.{% if runner_local_listen_tools %} `local_listen` is for a server.{% endif %}{% if runner_loopback_connect_tools %} `loopback_connect` is for its client.{% endif %}{% if runner_local_listen_tools and runner_loopback_connect_tools %} A self-calling harness needs both. Omit `ports` for OS assignment: `capability_request: {local_listen: {}, loopback_connect: {}}`; otherwise list exact ports.{% endif %}{% if runner_direct_ip_tools %} `direct_ip` is for UDP or proxy bypass; scope it to the destination.{% endif %}

{% if runner_socket_tools %}Use `socket_paths` for an exact local socket or `host_resources` for a catalog service.{% endif %}

{% if profile_has_verify %}`VERIFY_UNVERIFIABLE` means the boundary prevented a verdict. Follow its remedy; another check does not satisfy the gate.{% endif %}

A refusal names its `Code:` and the grant to reissue with; follow it. Listen, loopback, proxy, and direct IP are distinct capabilities; never combine `socks_proxy` with `direct_ip`. Missing paths and bad `cwd` are argument errors.
