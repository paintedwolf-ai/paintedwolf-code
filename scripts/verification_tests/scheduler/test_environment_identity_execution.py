"""The declared environment must describe the checkout, not the call."""

import unittest

import verification_plan as planning
from verification_reuse import environment_identity


# Values a host stamps per invocation: a fresh address, a boot's capture paths,
# a session's own directory. Any of these inside the declared environment gives
# every request a different compatibility digest, which ends batch sharing and
# result reuse without any visible failure.
PER_INVOCATION = {
    "HTTP_PROXY": ("http://127.0.0.1:51526", "http://127.0.0.1:51845"),
    "HTTPS_PROXY": ("http://127.0.0.1:51526", "http://127.0.0.1:51845"),
    "http_proxy": ("http://127.0.0.1:51526", "http://127.0.0.1:51845"),
    "https_proxy": ("http://127.0.0.1:51526", "http://127.0.0.1:51845"),
    "ALL_PROXY": ("socks5h://127.0.0.1:51527", "socks5h://127.0.0.1:51846"),
    "NO_PROXY": ("", ""),
    "GIT_SSH_COMMAND": ("ssh -o ProxyCommand=a", "ssh -o ProxyCommand=b"),
    "NODE_USE_ENV_PROXY": ("1", "1"),
    "LYCAON_DEBUG_SESSION_DIR": ("/c/debug/20260920T050553Z", "/c/debug/20260920T061201Z"),
    "LYCAON_LOG_FILE": ("/c/debug/a/sidecar.log", "/c/debug/b/sidecar.log"),
    "LYCAON_PROXY_TOKEN": ("6b2f…", "9d41…"),
    "LYCAON_SOCKS_PROXY": ("127.0.0.1:51527", "127.0.0.1:51846"),
    "LYCAON_API_TOKEN": ("token-a", "token-b"),
    "LYCAON_CONTROL_STDIN": ("1", "1"),
}

CHECKOUT_ENV = {
    "PATH": "/usr/bin:/bin",
    "HOME": "/Users/fixture",
    "GOFLAGS": "-mod=mod",
    "PW_TEST_WORKERS": "6",
}


class EnvironmentIdentityTests(unittest.TestCase):
    def test_two_invocations_of_one_checkout_share_a_digest(self):
        """Batch sharing and Go result reuse both key on this digest."""
        first = dict(CHECKOUT_ENV)
        second = dict(CHECKOUT_ENV)
        for name, (a, b) in PER_INVOCATION.items():
            first[name] = a
            second[name] = b

        first_declared = planning.execution_environment(first)
        second_declared = planning.execution_environment(second)
        self.assertEqual(
            first_declared,
            second_declared,
            "the declared environment differs between two calls in one checkout; "
            "a per-invocation value reached it",
        )
        self.assertEqual(environment_identity(first_declared), environment_identity(second_declared))

    def test_declared_environment_keeps_the_checkout_inputs(self):
        declared = planning.execution_environment(dict(CHECKOUT_ENV))
        for name in ("PATH", "HOME", "GOFLAGS"):
            self.assertIn(name, declared, f"{name} describes the checkout and must reach shared stages")

    def test_no_declared_name_or_prefix_admits_a_per_invocation_value(self):
        """A name list drifts; this asserts the rule instead."""
        declaration = planning.catalog()["environment"]
        admitted = [
            name
            for name in PER_INVOCATION
            if planning.declared_variable(name, declaration)
            and name not in planning.SCHEDULER_ENV
            and name not in planning.NETWORK_ENV
        ]
        self.assertEqual(
            admitted,
            [],
            "the catalog declares names or prefixes that admit per-invocation values",
        )

    def test_verification_declares_no_network_route(self):
        """Fixtures are repository-local; a live front door is not an input."""
        routed = planning.execution_environment(
            {**CHECKOUT_ENV, "HTTPS_PROXY": "http://127.0.0.1:9", "ALL_PROXY": "socks5h://127.0.0.1:10"}
        )
        self.assertNotIn("HTTPS_PROXY", routed)
        self.assertNotIn("ALL_PROXY", routed)


if __name__ == "__main__":
    unittest.main()
