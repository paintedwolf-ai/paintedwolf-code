# Platform targets, rollback, and builds

| Platform | Safe pre-production target | Rollback | Where the build runs |
|---|---|---|---|
| Vercel | Preview deploy | Restore or promote an eligible earlier deployment | Remotely, normally |
| Netlify | Preview deploy | Restore or promote an eligible earlier deployment | Remotely, normally |
| Cloudflare Workers (Wrangler) | Preview URLs and version upload/deploy flows, depending on project configuration | Roll back to a retained version; deleted or incompatibly changed bindings do not come back | Wrangler may upload a local build produced by project tooling |
| Fly.io | A separate staging app selected explicitly with `-a`; there is no implicit preview, and a deploy goes straight to the named app | Redeploy a retained prior image; database and configuration changes are not restored | A local or a remote builder |

On every platform, environment variables are scoped per environment. Check the target environment's set, not the preview's, before promoting.

Most platforms reverse neither migrations nor external state. Say whether a rollback covers code only or also configuration and data.
