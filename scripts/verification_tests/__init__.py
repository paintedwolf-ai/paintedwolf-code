"""Verification runner behavior and integration tests."""

import os

# Admission expectations describe a shared host; dedicated-host cases opt in.
os.environ.pop("PW_TEST_HOST", None)
