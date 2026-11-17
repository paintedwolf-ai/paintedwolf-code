"""Publish complete notice files for concurrent build and snapshot readers."""

from pathlib import Path
import shutil
import sys
import tempfile


def stage(source, destinations):
    with source.open("rb") as contents:
        for destination in destinations:
            contents.seek(0)
            with tempfile.NamedTemporaryFile(dir=destination.parent, prefix=".THIRD-PARTY-NOTICES.md.",
                                             delete=False) as output:
                staged = Path(output.name)
                try:
                    shutil.copyfileobj(contents, output)
                    output.close()
                    staged.chmod(0o644)
                    staged.replace(destination)
                finally:
                    staged.unlink(missing_ok=True)


if __name__ == "__main__":
    stage(Path(sys.argv[1]), map(Path, sys.argv[2:]))
