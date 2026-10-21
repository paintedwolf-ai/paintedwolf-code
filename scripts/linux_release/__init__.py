"""Linux release contracts and packaging adapters."""

import importlib

# ./task entry points import this package from the repository root as
# scripts.linux_release; the unittest runner discovers it from scripts/ as
# linux_release. Sibling script packages resolve against the same root.
SCRIPTS_PACKAGE = __name__.rpartition("linux_release")[0]


def scripts_module(name: str):
    """A module from a sibling package under scripts/, such as harness.atspi_client."""
    return importlib.import_module(SCRIPTS_PACKAGE + name)
