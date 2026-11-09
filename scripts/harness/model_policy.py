"""Apply an explicit harness model without replacing unrelated routing policy."""

import copy
import json
import os
import sys
import urllib.parse
import urllib.request


def select_model(current, provider, model):
    policy = copy.deepcopy(current)
    provider = provider or policy.get("coordinator", {}).get("provider_id", "")
    if not provider or not model:
        raise ValueError("harness model selection requires a provider and model")
    selection = {"provider_id": provider, "model": model}
    policy["coordinator"] = selection.copy()
    policy["lite"] = selection.copy()
    pool = policy.setdefault("agent_pool", {})
    pool.setdefault("selection", "first")
    pool["models"] = [selection.copy()]
    return policy


def configure(project, provider, model, request):
    path = "/v1/settings/model-policy?" + urllib.parse.urlencode({"project_id": project})
    policy = select_model(request(path), provider, model)
    request(path, policy)
    return policy["coordinator"]["provider_id"]


def main():
    origin = os.environ["LYCAON_E2E_API_URL"]
    headers = {"Authorization": "Bearer " + os.environ["LYCAON_E2E_TOKEN"],
               "Content-Type": "application/json"}

    def request(path, payload=None):
        data = None if payload is None else json.dumps(payload).encode()
        req = urllib.request.Request(origin + path, data=data, headers=headers,
                                     method="GET" if payload is None else "PATCH")
        with urllib.request.urlopen(req, timeout=60) as response:
            return json.load(response)

    print(configure(*sys.argv[1:], request))


if __name__ == "__main__":
    main()
