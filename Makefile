.PHONY: build run serve test vet fmt build-ui dev-backend dev-frontend clean clean-tree image deploy deploy-diff

build:
	go build -o bin/ego ./cmd/ego

run: build
	./bin/ego run $(ARGS)

serve: build
	./bin/ego serve

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l .

build-ui:
	cd ui && npm ci && npm run build

dev-backend:
	go run ./cmd/ego serve

dev-frontend:
	cd ui && npm run dev

clean:
	rm -rf bin ui/dist

# ── release: ko image + Helm (charts/ego) ─────────────────────────
# Deployment values live in deploy/values.yaml (gitignored; copy
# deploy/values.example.yaml). A dev image is versioned from the last release
# tag, as git describe prints it: 0.1.40 on the tag itself, 0.1.40-2-gabc1234
# two commits after it, so a version always reads against a release. This
# deployment's registry and kube context go in deploy/local.mk (gitignored):
#   KO_DOCKER_REPO = ghcr.io/your-org/ego
#   KUBE_CONTEXT   = your-cluster-context
-include deploy/local.mk
VERSION       ?= $(shell git describe --tags --always --match 'v*' | sed 's/^v//')
KO_DOCKER_REPO ?= ghcr.io/your-org/ego
# UI path prefix baked into the build ("/" = unprefixed, what the routes expect).
UI_BASE_PATH  ?= /
KUBE_CONTEXT  ?= $(shell kubectl config current-context)
NAMESPACE     ?= ego
RELEASE       ?= ego
VALUES        ?= deploy/values.yaml
HELM          = helm --kube-context $(KUBE_CONTEXT) -n $(NAMESPACE)

# The tag carries the commit hash, so the tree must be that commit.
clean-tree:
	@test -z "$$(git status --porcelain)" || { echo "working tree is dirty: commit first (the image tag names HEAD)"; git status --short; exit 1; }

# Build the UI into ui/dist (the binary embeds it), then build and push with ko.
image: clean-tree
	cd ui && npm ci && VITE_API_BASE_URL="$(patsubst %/,%,$(UI_BASE_PATH))" npm run build -- --base="$(UI_BASE_PATH)"
	git checkout -- ui/dist/.gitkeep  # vite empties dist; keep the tree clean
	VERSION=$(VERSION) KO_DOCKER_REPO=$(KO_DOCKER_REPO) ko build --platform=linux/amd64 --bare --tags=$(VERSION) ./cmd/ego
	@mkdir -p bin && echo "$(KO_DOCKER_REPO):$(VERSION)" > bin/.image
	@echo "image: $(KO_DOCKER_REPO):$(VERSION)"

# The tag to deploy: HEAD's image by default, or a published release (make deploy TAG=0.1.40).
TAG ?= $(VERSION)

# `make deploy` once rolled the release to a tag whose push had failed, and the pod sat in ImagePullBackOff. A HEAD
# tag is deployed only when `make image` recorded pushing it; a release tag is taken as published.
check-image:
	@test "$(TAG)" != "$(VERSION)" || grep -qx "$(KO_DOCKER_REPO):$(VERSION)" bin/.image 2>/dev/null || { echo "no image pushed for $(VERSION): run make image first"; exit 1; }

# Show what `make deploy` would change on the cluster.
deploy-diff: check-image
	$(HELM) template $(RELEASE) charts/ego -f $(VALUES) --set image.tag=$(TAG) | kubectl --context $(KUBE_CONTEXT) -n $(NAMESPACE) diff -f - || true

# Roll the release to $(TAG). --atomic rolls back to the previous revision when the new pod is not Ready in time,
# so a bad image or a crash loop never leaves the service down.
deploy: check-image
	$(HELM) upgrade --install $(RELEASE) charts/ego -f $(VALUES) --set image.tag=$(TAG) --atomic --wait --timeout 3m
