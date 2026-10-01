"""Apply the isolated application's model policy with durable setup diagnostics."""
import argparse
import json
import os
import pathlib
import urllib.error
import urllib.request
from progress import save
from application_http import open_request


def configure(url, token, coordinator, worker, output):
    operation = 'read'
    headers = {'Authorization':'Bearer '+token, 'Content-Type':'application/json'}
    endpoint = url+'/v1/settings/model-policy'
    try:
        with open_request(urllib.request.Request(endpoint, headers=headers)) as response:
            policy = json.load(response)
        policy['coordinator'] = coordinator
        policy['agent_pool'] = {**policy.get('agent_pool', {}), 'selection':'first', 'models':[worker]}
        operation = 'apply'
        request = urllib.request.Request(endpoint, data=json.dumps(policy).encode(), method='PATCH', headers=headers)
        with open_request(request) as response:
            save(output/'model-policy-response.json', json.load(response))
        return True
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode('utf-8', errors='replace')
        try:
            detail = json.loads(raw)
        except ValueError:
            detail = raw
        transient = exc.code == 429 or 500 <= exc.code < 600
        failure = {'kind':'harness' if transient else 'application',
                   'code':'preparation_transport' if transient else 'model_policy_rejected', 'retryable':transient}
        if exc.code == 503 and isinstance(detail, dict) and detail.get('code') == 'provider_catalog_unavailable':
            failure = {'kind':'provider', 'code':'provider_catalog_unavailable', 'retryable':True}
        save(output/'setup-failure.json', {'operation':operation, 'http_status':exc.code, 'failure':failure, 'response':detail})
        return False
    except (urllib.error.URLError, OSError) as exc:
        save(output/'setup-failure.json', {'operation':operation, 'failure':{
            'kind':'harness','code':'preparation_transport','retryable':True}, 'diagnostic':str(exc)})
        return False


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--url', required=True)
    parser.add_argument('--provider', required=True)
    parser.add_argument('--model', required=True)
    parser.add_argument('--worker-provider', required=True)
    parser.add_argument('--worker-model', required=True)
    parser.add_argument('--out', type=pathlib.Path, required=True)
    args = parser.parse_args()
    if not configure(args.url, os.environ['LYCAON_API_TOKEN'], {'provider_id':args.provider,'model':args.model},
                     {'provider_id':args.worker_provider,'model':args.worker_model}, args.out):
        raise SystemExit('Application model-policy setup failed; inspect setup-failure.json.')


if __name__ == '__main__':
    main()
