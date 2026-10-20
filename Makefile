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
# deploy/values.example.yaml). Tags are <BASE_VERSION>.<commit count>-<sha>,
# so every image names the commit it was built from. This deployment's
# registry and kube context go in deploy/local.mk (gitignored), e.g.
#   KO_DOCKER_REPO = ghcr.io/your-org/ego
#   KUBE_CONTEXT   = your-cluster-context
-include deploy/local.mk
BASE_VERSION  ?= 0.24
VERSION       ?= $(BASE_VERSION).$(shell git rev-list --count HEAD)-$(shell git rev-parse --short HEAD)
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
	@echo "image: $(KO_DOCKER_REPO):$(VERSION)"

# Show what `make deploy` would change on the cluster.
deploy-diff:
	$(HELM) template $(RELEASE) charts/ego -f $(VALUES) --set image.tag=$(VERSION) | kubectl --context $(KUBE_CONTEXT) -n $(NAMESPACE) diff -f - || true

# Roll the release to $(VERSION) (the image `make image` pushed for HEAD).
deploy:
	$(HELM) upgrade --install $(RELEASE) charts/ego -f $(VALUES) --set image.tag=$(VERSION) --wait --timeout 5m
