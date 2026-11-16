# Captured external files design preview

This harness-only proposal renders additional location roots in the real Files tree and opens local files in the normal editor and illustrative remote capture records through the existing read-only comparison viewer. It uses the normal shell, theme, icons, tabs, source reader, and Files/Review control. There is no replacement HTML page or stylesheet.

The fixture does not implement external source admission, remote connections, capture ingestion, or permission grants. Its three backing directories are disposable local directories registered with the harness. Their labels select explicit presentation fixtures only when both harness mode and `?files-location-preview=1` are enabled. They never determine host authority.

Start an isolated stack with `HARNESS_KEEP_STATE=1 LYCAON_E2E_STATE_DIR=<state-directory> ./task den:harness`. With the printed API URL, run:

```sh
python3 lycaon-den/e2e/fixtures/seed-files-location-preview.py <api-url> <disposable-fixture-directory>
```

Open the printed frontend URL with `?files-location-preview=1`, select **Website deployment**, and open Files. The local project root works normally. The other roots illustrate:

- `ops/deploy.yaml`: a local file outside the project, using the normal editable document and save behavior. Its location does not imply read-only access.
- `staging-01/etc/nginx/nginx.conf`: captured remote before and after versions.
- `staging-01/etc/nginx/upstream.conf`: after-only capture, without asserting creation.
- `staging-01/etc/nginx/mime.types`: reported modification with no captured bytes.

The remote root is labeled **Captured**, with its host name retained. Capture details show the illustrative time and originating action. Captured files use composed `diff` buffers. They have no live editor document, restore action, or Current action. Fixture contents and attribution text are illustrative, not evidence of a real agent invocation. Review remains the existing product surface; this preview does not ingest the fixtures as real remote change records.

## Proposed outside-project browsing policy

Permission scope and project membership remain separate. Granting access adds a collapsed location in Files, not an attached project root. Agent-modified paths may be revealed automatically; expanding a local directory shows all of its immediate children, including untouched files, in bounded pages. A whole-disk grant must not trigger recursive discovery, content indexing, whole-tree snapshots, or disk-wide watching. Only requested directories and open documents are observed or refreshed. Broad access does not imply broad background work.

Local files use normal editing under the applicable host permissions. Review shows recorded changes independently of the directory browser. Overlapping grants should not duplicate locations, and losing agent access must not discard recorded changes or silently redefine the person's editor access. Permission grants do not enroll a folder in project-wide search; explicitly attaching a project folder remains a separate action with its existing indexing semantics.

This is the proposed production contract, not a claim about the fixture backend. The current Files tree reads retained indexed structure generations. This mockup registers tiny local fixture roots to reuse that renderer; production grant locations require host support for bounded directory browsing independent of project-root enrollment. Remote locations contain only captured paths and versions; they do not imply a listing of the remote filesystem.
