"""Native source owners and the verification lanes that exercise them."""
from pathlib import PurePosixPath

# Manifests, fixtures, generated inputs, and build scripts keep the full gate.
OWNERS = {
    "lycaon-den/src-tauri/src/": {"lanes": {"native", "frontend"}, "go": ()},
    "lycaon/internal/decide/native/src/": {"lanes": {"native"}, "go": ("lycaon/internal/decide",)},
    "lycaon/internal/documentcore/native/src/": {"lanes": {"native"}, "go": ("lycaon/internal/documentcore",)},
}


def owner(path):
    if PurePosixPath(path).suffix != ".rs":
        return None
    return next((value for prefix, value in OWNERS.items() if path.startswith(prefix)), None)
