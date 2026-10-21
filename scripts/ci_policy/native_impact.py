"""Native source dependencies and the verification lanes that exercise them."""
from pathlib import PurePosixPath

# Manifests, fixtures, generated inputs, and build scripts keep the full gate.
DEPENDENCIES = {
    "lycaon-den/src-tauri/src/": {"lanes": {"native", "frontend"}, "go": ()},
    "lycaon/internal/decide/native/src/": {"lanes": {"native"}, "go": ("lycaon/internal/decide",)},
    "lycaon/internal/documentcore/native/src/": {"lanes": {"native", "frontend"}, "go": ("lycaon/internal/documentcore",)},
}


def dependencies(path):
    if PurePosixPath(path).suffix != ".rs":
        return None
    return next((value for prefix, value in DEPENDENCIES.items() if path.startswith(prefix)), None)
