"""Cancel this run once the merge queue removes its group.

When a group is invalidated, GitHub deletes its gh-readonly-queue branch but
lets the group's runs continue. Each long merge-group job runs this in the
background: it reads its own branch with git ls-remote, which spends no REST
API quota, and makes one API call to cancel the run once the branch is gone.
"""
import base64
import os
import subprocess
import time

from .github import api, repository

QUEUE_REFS = 'refs/heads/gh-readonly-queue/'
INTERVAL_SECONDS = 60


def branch_exists(url, ref, token=''):
    """True or False from the remote's answer; None when the remote could not answer."""
    environment = dict(os.environ, GIT_TERMINAL_PROMPT='0')
    if token:
        # Through the environment, so the token never appears in a process's arguments.
        credential = base64.b64encode(f'x-access-token:{token}'.encode()).decode()
        environment.update(GIT_CONFIG_COUNT='1', GIT_CONFIG_KEY_0='http.extraHeader',
                           GIT_CONFIG_VALUE_0=f'AUTHORIZATION: basic {credential}')
    try:
        result = subprocess.run(['git', 'ls-remote', '--exit-code', url, ref], env=environment,
                                capture_output=True, text=True, timeout=30)
    except subprocess.TimeoutExpired:
        return None
    # --exit-code reports a missing ref as 2; any other failure is no answer.
    return {0: True, 2: False}.get(result.returncode)


def watch(ref, url, cancel, exists=branch_exists, sleep=time.sleep, interval=INTERVAL_SECONDS):
    """Poll until the branch is gone, then cancel; an unanswered poll never cancels."""
    while True:
        sleep(interval)
        if exists(url, ref) is False:
            print(f'{ref} is gone; the merge queue removed this group, so this run stops.', flush=True)
            cancel()
            return


def main():
    ref = os.environ['GITHUB_REF']
    if not ref.startswith(QUEUE_REFS):
        raise SystemExit(f'{ref} is not a merge-queue branch')
    url = f"{os.environ['GITHUB_SERVER_URL']}/{os.environ['GITHUB_REPOSITORY']}"
    token = os.environ.get('GH_TOKEN', '')
    run = os.environ['GITHUB_RUN_ID']
    watch(ref, url, lambda: api(f'{repository()}/actions/runs/{run}/cancel', 'POST'),
          exists=lambda url, ref: branch_exists(url, ref, token))


if __name__ == '__main__':
    main()
