#!/usr/bin/env bash
# Publish a release from this machine, without GitHub Actions: the four binaries with checksums on a GitHub
# release, the multi-arch image on ghcr.io, and the Helm chart as an OCI artifact. It does what
# .github/workflows/release.yml does, minus the keyless signatures (they need the workflow's OIDC identity).
#
#   scripts/release-local.sh v0.1.51
#
# Run it in a clean checkout of the commit to release (a throwaway `git worktree add --detach` is the safest);
# the tag is created on HEAD when it does not exist yet. Needs go, node, ko, helm, gh, git, and a docker/ko login to
# ghcr.io. GH_USER, when set, names the gh account to push and publish with (otherwise the active one is used).
set -euo pipefail

tag="${1:-}"
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "usage: $0 vX.Y.Z" >&2; exit 2; }
version="${tag#v}"
minor="${version%.*}"

[[ -z "$(git status --porcelain)" ]] || { echo "working tree is dirty: release from a clean checkout" >&2; exit 1; }
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  [[ "$(git rev-parse "$tag^{commit}")" == "$(git rev-parse HEAD)" ]] || { echo "$tag is not HEAD" >&2; exit 1; }
fi

mod="$(go list -m)"
repo="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
repo="${repo,,}"
owner="${repo%%/*}"
if [[ -n "${GH_USER:-}" ]]; then
  GH_TOKEN="$(gh auth token --user "$GH_USER")"
else
  GH_TOKEN="$(gh auth token)"
  GH_USER="$(gh api user -q .login)"
fi
export GH_TOKEN
gh release view "$tag" >/dev/null 2>&1 && { echo "release $tag already exists" >&2; exit 1; }

push() { git -c credential.helper= -c "credential.helper=!f() { echo username=$GH_USER; echo password=$GH_TOKEN; }; f" push "$@"; }

echo "== UI"
(cd ui && npm ci --silent && npm run build)

echo "== checks"
test -z "$(gofmt -l .)" || { gofmt -l .; echo "gofmt" >&2; exit 1; }
go vet ./...
go test -count=1 ./...
(cd ui && npx tsc -b)

echo "== binaries"
rm -rf dist && mkdir dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  goos="${target%/*}"; goarch="${target#*/}"
  name="ego_${version}_${goos}_${goarch}"
  mkdir -p "dist/$name"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath \
    -ldflags "-s -w -X ${mod}/internal/api.Version=${version}" \
    -o "dist/$name/ego" ./cmd/ego
  cp LICENSE README.md "dist/$name/"
  (cd dist && tar -czf "$name.tar.gz" "$name" && rm -rf "$name")
done
(cd dist && shasum -a 256 ego_* > checksums.txt)

echo "== tag"
git rev-parse -q --verify "refs/tags/$tag" >/dev/null || git tag "$tag"
push origin "$tag"

echo "== image"
VERSION="$version" KO_DOCKER_REPO="ghcr.io/$repo" ko build --platform=linux/amd64,linux/arm64 --bare \
  --tags="$version,$minor,latest" ./cmd/ego

echo "== chart"
helm lint charts/ego
helm package charts/ego --version "$version" --app-version "$version" --destination dist
printf '%s' "$GH_TOKEN" | helm registry login ghcr.io --username "$GH_USER" --password-stdin
helm push "dist/ego-${version}.tgz" "oci://ghcr.io/${owner}/charts"

echo "== release"
gh release create "$tag" dist/*.tar.gz dist/checksums.txt --title "$tag" --generate-notes --verify-tag

echo "released $tag: ghcr.io/$repo:$version, oci://ghcr.io/${owner}/charts/ego:$version"
