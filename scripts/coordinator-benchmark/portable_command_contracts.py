"""Frozen portable-command acceptance inputs; this module never executes programs."""
import pathlib
from focused_outcomes import read_json

ROOT = pathlib.Path(__file__).resolve().parents[2]
SUITE = ROOT / 'lycaon/test/fixtures/eval/portable-command/suite.json'


def load_suite(path=SUITE):
    """Read independent fixture inputs, without importing application execution code."""
    suite = read_json(path)
    if not isinstance(suite, dict) or suite.get('version') != 1:
        raise ValueError('unsupported portable-command fixture suite')
    return suite
