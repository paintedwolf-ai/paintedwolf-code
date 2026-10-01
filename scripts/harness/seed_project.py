"""Register the harness fixture without replacing work in a preserved state."""

import json
import os
from pathlib import Path
import shutil
import sys
import urllib.parse
import urllib.request


def ensure_project(fixture, destination, projects, create):
    destination = destination.resolve()
    for project in projects:
        if any(Path(root["path"]).resolve() == destination for root in project.get("roots", [])):
            return project
    if not destination.exists():
        shutil.copytree(fixture, destination)
    if not destination.is_dir():
        raise ValueError("harness project path is not a directory")
    return create({"name": "Harness", "roots": [{"path": str(destination)}]})


def main():
    fixture, destination = map(Path, sys.argv[1:])
    url = os.environ["LYCAON_E2E_API_URL"] + "/v1/projects"
    headers = {"Authorization": "Bearer " + os.environ["LYCAON_E2E_TOKEN"]}

    def request(payload=None, query=""):
        data = None if payload is None else json.dumps(payload).encode()
        req = urllib.request.Request(url + query, data=data, headers={**headers, "Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=60) as response:
            return json.load(response)

    def listed():
        cursor = None
        while True:
            page = request(query="?limit=200" + ("&cursor=" + urllib.parse.quote(cursor) if cursor else ""))
            yield from page["projects"]
            cursor = page.get("next_cursor")
            if not cursor:
                return

    print(json.dumps(ensure_project(fixture, destination, listed(), request)))


if __name__ == "__main__":
    main()
