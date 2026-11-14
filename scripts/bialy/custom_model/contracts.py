"""The published training contracts, checked with JSON Schema."""
from functools import lru_cache
import json
from pathlib import Path

from jsonschema import Draft202012Validator

ROOT = Path(__file__).resolve().parent / "contracts"


@lru_cache(maxsize=None)
def _schema(name):
    return json.loads((ROOT / (name + ".schema.json")).read_text())


@lru_cache(maxsize=None)
def _validator(name, definition):
    schema = _schema(name)
    if definition:
        schema = {"$schema": schema["$schema"], "$defs": schema["$defs"], "$ref": "#/$defs/" + definition}
    return Draft202012Validator(schema)


def check(name, value, definition=None):
    """Raise ValueError naming the contract when value does not follow it."""
    error = next(iter(_validator(name, definition).iter_errors(value)), None)
    if error is not None:
        raise ValueError("%s contract violated" % (definition or name)) from ValueError(error.message)
