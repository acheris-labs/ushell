# Distributing ush

The `ush` CLI ships as a Homebrew cask: one signed, notarized binary, Apple
silicon only. The rendezvous (`ushd`) ships as an image from `main.yml`, not
this way.

```sh
brew install --cask acheris-labs/tools/ush
```

## Releasing

```sh
make release                 # tags the next patch (v0.1.0 -> v0.1.1) and pushes it
make release VERSION=0.2.0   # or a version of your choosing
```

It refuses unless you are on `main`, with nothing uncommitted, level with
`origin/main`, and the tag is new. Pushing the tag starts the `release`
workflow, which then:

1. Runs the Go tests.
2. Imports the Developer ID certificate into a temporary keychain.
3. Builds `cmd/ush` for darwin/arm64, signs it with the hardened runtime, and
   notarizes it as a zip (`make notarize`), then checks Gatekeeper accepts it as
   notarized (`make verify`).
4. Creates the GitHub release with `ush-<version>.zip` and its `.sha256`, with
   notes from this version's `CHANGELOG.md` section when there is one.
5. Renders `packaging/ush-cask.rb.tmpl` and pushes it to
   `acheris-labs/homebrew-tools` as `Casks/ush.rb`.

`workflow_dispatch` does the same from a version string, tagging as it goes.

## Credentials

| Secret | Where | What it is |
|--------|-------|------------|
| `DEVID_CERT_P12`, `DEVID_CERT_PASSWORD`, `DEVID_IDENTITY` | org, scoped to this repo | Developer ID Application certificate, shared with pr-mon, newt and tracker |
| `APPLE_ID`, `APPLE_TEAM_ID`, `APPLE_APP_PASSWORD` | org, scoped to this repo | For notarization |
| `HOMEBREW_TAP_SSH_KEY` | this repo | Private half of the "ushell release" deploy key on the tap, with write access to it and nothing else |

The org secrets are set up from pr-mon (`make setup-secrets`); a new repo only
needs adding to their scope.

## Building by hand

```sh
make dist                                  # bin/dist/ush, unsigned
make notarize SIGN_ID="Developer ID Application: … (TEAMID)" NOTARY_PROFILE=…
make verify
```

## Notes

- **No stapling.** `stapler` only works on bundles, disk images and installer
  packages, so a bare binary is notarized but carries no ticket; Gatekeeper
  fetches it online the first time `ush` runs.
- **Notarization failures** come back as a log:
  `xcrun notarytool log <id> --keychain-profile ush-notary`.
