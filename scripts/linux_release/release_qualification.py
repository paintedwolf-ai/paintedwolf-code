"""Linux publication is admitted by the same exact-source Qualification record as every release."""

from __future__ import annotations

import os


def require(source_commit: str):
    from ci_policy.verification_verdict import require_qualification
    return require_qualification(os.environ["GITHUB_REPOSITORY"], source_commit)
