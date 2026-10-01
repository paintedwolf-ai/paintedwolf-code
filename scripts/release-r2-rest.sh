# Listings establish object existence while GET misses may remain cached.

r2_rest_require_env() {
  : "${R2_BUCKET:?missing R2_BUCKET}"
  : "${CLOUDFLARE_ACCOUNT_ID:?missing CLOUDFLARE_ACCOUNT_ID}"
  : "${CLOUDFLARE_API_TOKEN:?missing CLOUDFLARE_API_TOKEN}"
}

r2_rest_file_md5() {
  python3 -c 'import hashlib, sys; print(hashlib.md5(open(sys.argv[1], "rb").read()).hexdigest())' "$1"
}

r2_rest_object_stat() {
  local key="$1"
  r2_rest_require_env
  curl --silent --show-error --connect-timeout 10 --max-time 30 --get \
    --data-urlencode "prefix=${key}" \
    --data-urlencode "per_page=20" \
    -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" \
    "https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}/r2/buckets/${R2_BUCKET}/objects" \
    | python3 -c '
import json, sys
key = sys.argv[1]
doc = json.load(sys.stdin)
if not doc.get("success"):
    sys.stderr.write("error: R2 list failed: %s\n" % json.dumps(doc.get("errors")))
    raise SystemExit(1)
result = doc.get("result") or []
if isinstance(result, dict):
    result = result.get("objects") or result.get("keys") or []
for obj in result:
    if isinstance(obj, dict) and obj.get("key") == key:
        json.dump(obj, sys.stdout)
        break
' "${key}"
}

r2_rest_etag() {
  python3 -c 'import json, sys
raw = sys.stdin.read().strip()
if not raw:
    raise SystemExit(0)
obj = json.loads(raw)
etag = str(obj.get("etag") or "").strip().strip("\"")
print(etag)
'
}

r2_rest_get() {
  local key="$1" output="$2"
  r2_rest_require_env
  curl --silent --show-error --connect-timeout 10 --max-time 30 --output "${output}" --write-out '%{http_code}' \
    -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" \
    "https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}/r2/buckets/${R2_BUCKET}/objects/${key}"
}
