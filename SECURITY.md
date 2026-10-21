# Security policy

## Reporting a vulnerability

Please report it privately through GitHub: open the repository's **Security** tab and choose **Report a
vulnerability**. Do not open a public issue for it. A report that says what you found, how to reproduce it and
which version it affects is enough; we acknowledge it within a few working days and keep you informed until it is
fixed.

## Supported versions

Only the latest release receives fixes. ego ships as one static binary, a container image and a Helm chart, all
published from the same tag; update to the newest one.

## What to expect from ego

- The OTLP receivers always require a bearer token; with none configured they refuse every push.
- `ego serve` listens on loopback by default and refuses other addresses without a login (`--session-secret`) unless
  `--no-auth` says so.
- Releases are built from a tagged commit, in CI up to v0.1.50 and on a maintainer's machine after that.
  The checksums file and the container image of the CI-built releases carry keyless cosign signatures; see the README
  for how to verify them. Later releases and the Helm chart are not signed yet.
