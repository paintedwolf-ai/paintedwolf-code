"""Unit tests for the standalone CF02 catalog_feed_search package."""

from __future__ import annotations

from pathlib import Path
import sys

_PARENT_DIR = str(Path(__file__).resolve().parents[2])
if _PARENT_DIR not in sys.path:
    sys.path.insert(0, _PARENT_DIR)
