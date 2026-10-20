# ego Helm chart

Reality testing for AI agents: ego scores agent behaviour from OpenTelemetry traces. This chart runs the single
`ego` binary (UI, REST API, OTLP receivers) with a SQLite database on a PersistentVolumeClaim.

## Install

The chart and the image are published to GitHub Container Registry on every release tag:

- chart: `oci://ghcr.io/triageagent-dev/charts/ego`
- image: `ghcr.io/triageagent-dev/ego` (linux/amd64 and linux/arm64)

Try it in a cluster without a login (reachable only inside the cluster, or through `kubectl port-forward`):

```bash
helm install ego oci://ghcr.io/triageagent-dev/charts/ego --version 0.1.36 \
  --set auth.enabled=false --set vertex.enabled=false
kubectl port-forward svc/ego 8001:8001      # UI and API
```

Point your OpenTelemetry exporter at `ego:4318` (OTLP/HTTP) or `ego:4317` (OTLP/gRPC).

`auth.enabled=false` serves every caller as admin, so only use it where the network limits who can reach the service.

## Production values

```yaml
auth:
  enabled: true
  githubClientId: <OAuth app client id>
  githubOrg: <your org>              # only active members may sign in
  publicUrl: https://ego.example.com # the OAuth callback is <publicUrl>/auth/callback
  githubClientSecret: ego-github-oauth   # Secret with GITHUB_CLIENT_SECRET
  sessionSecret: ego-session-secret      # Secret with SESSION_SECRET, at least 32 bytes
adminGithubUsers: [your-login]

otlpAuth:                 # senders must then send "Authorization: Bearer <token>"
  enabled: true
  secret: ego-otlp-token  # Secret with OTLP_TOKEN, at least 32 bytes

networkPolicy:            # lists who may reach 8001 (UI) and 4317/4318 (telemetry)
  enabled: true
  ingress: []             # see values.yaml for an example

httpRoute:                # Gateway API; the UI is served under /evals, "/" redirects there
  enabled: true
  hostname: ego.example.com
  parentRef: {name: <gateway>, namespace: <gateway namespace>, sectionName: https}
```

Create the Secrets first, for example:

```bash
kubectl create secret generic ego-session-secret --from-literal=SESSION_SECRET="$(openssl rand -hex 32)"
kubectl create secret generic ego-github-oauth --from-literal=GITHUB_CLIENT_SECRET=<secret>
```

An empty session secret is refused at start (the pod crash-loops) instead of silently serving without a login.

## Tenants

Admins create tenants and per-sender ingest tokens through the API (see the main README, "Tenants"); no chart value is
needed. Once a tenant has a token, the OTLP receivers require one. The shared `otlpAuth` token keeps working for a
trusted sender that labels its own tenants.

## Judges

Judge metrics need a model. On GKE the default is Vertex AI through Workload Identity (`vertex.project`,
`serviceAccount.annotations`); `vertex.enabled=false` leaves only the metrics that need no model. The Jev metrics read
`JEV_API_KEY` from the Secret named by `jevSecret` (optional). Interactive evaluation is capped per day
(`--eval-daily-tokens`, default 5M judge tokens).

## Verifying a release

The image and `checksums.txt` of a release are signed with cosign (keyless, GitHub Actions identity). The chart is not
signed yet.

```bash
cosign verify ghcr.io/triageagent-dev/ego:<version> \
  --certificate-identity-regexp 'https://github.com/triageagent-dev/ego/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

All values and their defaults are documented in `values.yaml`.
