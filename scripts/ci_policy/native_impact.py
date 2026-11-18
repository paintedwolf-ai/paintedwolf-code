"""Native source dependencies and the verification lanes that exercise them."""
from pathlib import PurePosixPath

# Manifests, fixtures, generated inputs, and build scripts keep the full gate.
# `area` is the change area lanes declare in `changes` of the verification plan.
DEPENDENCIES = {
    # The updater's install path is compiled only for macOS.
    "lycaon-den/src-tauri/src/update_service/": {"area": "updater-native", "go": ()},
    "lycaon-den/src-tauri/src/": {"area": "shell-native", "go": ()},
    "lycaon/internal/decide/native/src/": {"area": "decide-native", "go": ()},
    "lycaon/internal/documentcore/native/src/": {"area": "documentcore-native", "go": ("lycaon/internal/documentcore",)},
}


def dependencies(path):
    if PurePosixPath(path).suffix != ".rs":
        return None
    return next((value for prefix, value in DEPENDENCIES.items() if path.startswith(prefix)), None)
