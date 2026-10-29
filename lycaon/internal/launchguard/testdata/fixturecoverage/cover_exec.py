"""Apply the fixture overlay to cover's inputs; Go applies it to other tools."""

import json
import os
from pathlib import Path
import sys


def main():
    if len(sys.argv) < 3:
        raise SystemExit("fixture cover adapter requires overlay and Go tool")
    overlay, tool = Path(sys.argv[1]), Path(sys.argv[2])
    if not tool.is_absolute() or not tool.is_file():
        raise SystemExit("fixture cover adapter requires an absolute Go tool file")
    arguments = sys.argv[3:]
    if tool.name == "cover":
        replacements = json.loads(overlay.read_text())["Replace"]
        if not isinstance(replacements, dict):
            raise SystemExit("fixture cover overlay requires a Replace object")
        for original, backing in replacements.items():
            if not isinstance(original, str) or not isinstance(backing, str):
                raise SystemExit("fixture cover overlay paths must be strings")
            source, actual = Path(original), Path(backing)
            if (not source.is_absolute() or not actual.is_absolute()
                    or source.suffix != ".go" or source.name != actual.name
                    or not actual.is_file()):
                raise SystemExit("fixture cover overlay requires same-name physical Go sources")
        arguments = [replacements.get(argument, argument) for argument in arguments]
    os.execv(str(tool), [str(tool), *arguments])


if __name__ == "__main__":
    main()
