"""Create disposable files for the real Den source/editor presentation."""
import json
from pathlib import Path
import sys
import urllib.request

base = Path(sys.argv[2]).resolve()
files = {
    'staging-01/etc/nginx/upstream.conf': 'upstream website {\n    server 127.0.0.1:3000;\n    keepalive 32;\n}\n',
    'staging-01/etc/nginx/mime.types': '',
    'website/src/health.ts': 'export function health() {\n  return { status: "ready" };\n}\n',
    'website/package.json': '{"name":"website","private":true}\n',
    'website/README.md': '# Website deployment\n\nDeployment configuration is kept in ops.\n',
    'ops/deploy.yaml': '# Website deployment\nservice: website\nenvironment: staging\n\nhealthcheck:\n  path: /health\n  interval: 10s\n  timeout: 60s\n\nreplicas: 2\n',
    'staging-01/etc/nginx/nginx.conf': 'server {\n    listen 443 ssl;\n    server_name preview.example.test;\n\n    location / {\n        proxy_pass http://127.0.0.1:3000;\n        proxy_read_timeout 60s;\n    }\n}\n',
}
for relative, content in files.items():
    path = base / relative
    path.parent.mkdir(parents=True, exist_ok=True)
    if not path.exists():
        path.write_text(content)
payload = {'name': 'Website deployment', 'roots': [
    {'path': str(base / name), 'label': name, 'is_primary': name == 'website'}
    for name in ('website', 'ops', 'staging-01')
]}
req = urllib.request.Request(sys.argv[1] + '/v1/projects', data=json.dumps(payload).encode(), headers={
    'Authorization': 'Bearer e2e-playwright-token', 'Content-Type': 'application/json'
})
with urllib.request.urlopen(req, timeout=60) as response:
    project = json.load(response)
(base / 'project.json').write_text(json.dumps(project, indent=2))
print('Created preview project:', project['id'])
