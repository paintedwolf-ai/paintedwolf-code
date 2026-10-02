# Homebrew tap

Users install the app with:

```sh
brew install --cask <org>/tap/painted-wolf-code
```

Preview builds use the separate `painted-wolf-code@preview` token. The receipt
written by each cask selects the matching in-app update channel on first launch;
the setting remains user-controllable afterward. Both casks install the same app,
so each declares `conflicts_with` the other; switching channels means
uninstalling one cask before installing the other.

## How it works

- The cask lives in a **separate public repo** named `homebrew-<name>` (Homebrew
  convention, e.g. `homebrew-tap`). Homebrew taps require this dedicated
  repository structure so users can tap and install via `brew install --cask`.
- [`painted-wolf-code.rb.tmpl`](painted-wolf-code.rb.tmpl) here is the source of
  truth. On every `v*` tag, the `release` workflow renders it (version + Apple
  Silicon sha256 + download base URL + channel) and pushes the stable or preview
  cask to the tap repo. Never hand-edit a rendered file.
- Download URLs point at the Cloudflare R2 custom domain; the `.dmg` is
  Developer-ID-signed + notarized, so the sha256 in the cask is defense-in-depth,
  not the trust anchor (Gatekeeper is).

## One-time setup

1. Create a public repo `homebrew-tap` under the product org.
2. Add these to this repo's Actions config:
   - Variable `HOMEBREW_TAP_REPO` — `paintedwolf-ai/homebrew-tap`
   - Variable `DOWNLOAD_BASE_URL` — `https://downloads.paintedwolf.dev`
   - Secret `HOMEBREW_TAP_TOKEN` — a token with `contents:write` on the tap repo
3. The cask homepage is the marketing/download site, `https://paintedwolf.ai`.
