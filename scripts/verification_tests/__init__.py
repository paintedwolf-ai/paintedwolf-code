"""Verification runner behavior and integration tests."""

import os

# Admission expectations describe a shared host with a derived budget; the settings a host or
# runner exports would replace it, so cases that need one set it themselves.
for name in ("PW_TEST_HOST", "PW_TEST_CAPACITY", "PW_TEST_WORKERS"):
    os.environ.pop(name, None)
