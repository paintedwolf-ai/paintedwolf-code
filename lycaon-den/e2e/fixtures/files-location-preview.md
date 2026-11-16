# Captured external files design preview

This harness-only proposal renders additional location roots in the real Files tree and opens illustrative capture records through the existing read-only comparison viewer. It uses the normal shell, theme, icons, tabs, source reader, and Files/Review control. There is no replacement HTML page or stylesheet.

The fixture does not implement external source admission, remote connections, capture ingestion, or permission grants. Its three backing directories are disposable local directories registered with the harness. Their labels select explicit presentation fixtures only when both harness mode and `?files-location-preview=1` are enabled. They never determine host authority.

Start an isolated stack with `HARNESS_KEEP_STATE=1 LYCAON_E2E_STATE_DIR=<state-directory> ./task den:harness`. With the printed API URL, run:

```sh
python3 lycaon-den/e2e/fixtures/seed-files-location-preview.py <api-url> <disposable-fixture-directory>
```

Open the printed frontend URL with `?files-location-preview=1`, select **Website deployment**, and open Files. The local project root works normally. The other roots illustrate:

- `ops/deploy.yaml`: captured before and after versions.
- `staging-01/etc/nginx/nginx.conf`: captured remote before and after versions.
- `staging-01/etc/nginx/upstream.conf`: after-only capture, without asserting creation.
- `staging-01/etc/nginx/mime.types`: reported modification with no captured bytes.

Captured files use composed `diff` buffers. They have no live editor document, restore action, or Current action. Fixture contents and attribution text are illustrative, not evidence of a real agent invocation. Review remains the existing product surface; this preview does not ingest the fixtures as real remote change records.
