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

# R2's S3 API takes multipart uploads past wrangler's 300 MiB limit. Its
# credentials derive from the API token: the token ID is the access key and
# the token's SHA-256 is the secret.
r2_s3_require_env() {
  r2_rest_require_env
  command -v aws >/dev/null || { echo "error: aws CLI required for large R2 objects" >&2; return 1; }
  local id="" scope
  for scope in "accounts/${CLOUDFLARE_ACCOUNT_ID}" "user"; do
    id="$(curl --silent --show-error --connect-timeout 10 --max-time 30 \
      -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" \
      "https://api.cloudflare.com/client/v4/${scope}/tokens/verify" \
      | python3 -c 'import json, sys
doc = json.load(sys.stdin)
print((doc.get("result") or {}).get("id", "") if doc.get("success") else "")')"
    [[ -n "${id}" ]] && break
  done
  [[ -n "${id}" ]] || { echo "error: cannot resolve the Cloudflare API token ID for R2 S3 access" >&2; return 1; }
  local secret
  secret="$(printf '%s' "${CLOUDFLARE_API_TOKEN}" | python3 -c 'import hashlib, sys; print(hashlib.sha256(sys.stdin.buffer.read()).hexdigest())')"
  [[ -n "${GITHUB_ACTIONS:-}" ]] && echo "::add-mask::${secret}"
  export AWS_ACCESS_KEY_ID="${id}" AWS_SECRET_ACCESS_KEY="${secret}"
  export AWS_DEFAULT_REGION=auto AWS_EC2_METADATA_DISABLED=true
  # R2 rejects the CLI's default checksum headers on multipart parts.
  export AWS_REQUEST_CHECKSUM_CALCULATION=when_required AWS_RESPONSE_CHECKSUM_VALIDATION=when_required
  R2_S3_ENDPOINT="https://${CLOUDFLARE_ACCOUNT_ID}.r2.cloudflarestorage.com"
}

r2_rest_file_sha256() {
  python3 -c 'import hashlib, sys
digest = hashlib.sha256()
with open(sys.argv[1], "rb") as handle:
    for chunk in iter(lambda: handle.read(1 << 20), b""):
        digest.update(chunk)
print(digest.hexdigest())' "$1"
}

# Prints "<bytes> <sha256 metadata>" for an existing object.
r2_s3_object_identity() {
  aws s3api head-object --endpoint-url "${R2_S3_ENDPOINT}" --bucket "${R2_BUCKET}" --key "$1" \
    --query '[ContentLength, Metadata.sha256]' --output text
}
