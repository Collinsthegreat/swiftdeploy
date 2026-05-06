# SwiftDeploy - Declarative Container Stack Manager

SwiftDeploy generates Nginx and Docker Compose configuration from one
`manifest.yaml`, manages the container lifecycle, enforces OPA policy gates, and
records observability data for audit reports.

## Prerequisites

- Go 1.22+
- Docker 24+
- Docker Compose v2+

```bash
go build -o swiftdeploy ./cli/
chmod +x swiftdeploy
```

## Quick Start

```bash
# 1. Clone the repository
git clone https://github.com/Collinsthegreat/swiftdeploy.git
cd swiftdeploy

# 2. Build the API image
docker build -t swift-deploy-1-node:latest app/

# 3. Build the SwiftDeploy CLI
go build -o swiftdeploy ./cli/
chmod +x swiftdeploy

# 4. Generate configs from manifest
./swiftdeploy init

# 5. Run pre-flight checks
./swiftdeploy validate

# 6. Deploy the stack
./swiftdeploy deploy
```

## Subcommand Reference

### `./swiftdeploy init`
Parses `manifest.yaml` and generates `nginx.conf` and `docker-compose.yml`
from templates. Generated files are ignored by git and can always be recreated.

### `./swiftdeploy validate`
Runs 5 pre-flight checks:
1. `manifest.yaml` exists and is valid YAML
2. Required fields are present and non-empty
3. Docker image exists locally
4. Nginx port is available on the host
5. Generated `nginx.conf` syntax is valid

### `./swiftdeploy deploy`
Regenerates configs, boots the OPA sidecar, runs the infrastructure OPA policy
gate with disk, CPU, and memory context, starts the full stack with Docker
Compose, and waits up to 60 seconds for `/healthz`.

### `./swiftdeploy promote [canary|stable]`
Switches deployment mode by updating `manifest.yaml`, regenerating
`docker-compose.yml`, recreating only the application service, and confirming
the new mode through `/healthz`. Promotion from canary back to stable is gated
by the canary OPA policy using live `/metrics` data from the last 30 seconds.

### `./swiftdeploy status`
Shows a live dashboard refreshed every 3 seconds with request rate, p99 latency,
error rate, uptime, chaos state, and OPA policy compliance. It appends runtime
events to `history.jsonl`.

### `./swiftdeploy audit`
Reads `history.jsonl` and writes `audit_report.md` with a timeline, policy
violations, and mode changes.

### `./swiftdeploy teardown [--clean]`
Removes containers, networks, and volumes. `--clean` also deletes generated
`nginx.conf` and `docker-compose.yml`.

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/` | Welcome message, mode, version, timestamp |
| GET | `/healthz` | Status, mode, version, uptime |
| GET | `/metrics` | Prometheus text metrics |
| POST | `/chaos` | Canary-only degraded behavior simulation |

## Policy Enforcement

OPA runs with policies mounted from `./policies`. It is isolated from the
public ingress and bound to `127.0.0.1:8181` for local CLI policy queries only.

- `policies/infrastructure.rego` gates deploys using disk and CPU data.
- `policies/canary.rego` gates canary-to-stable promotion using error rate and p99 latency.
- Thresholds live in `policies/data.infrastructure.json` and `policies/data.canary.json`.

## Architecture

```text
Client -> Nginx (:8080) -> swiftdeploy-app (:3000, internal only)
CLI -> OPA (:8181 on 127.0.0.1 only)
manifest.yaml -> swiftdeploy init -> nginx.conf + docker-compose.yml
```

## Verification

```bash
rm -f nginx.conf docker-compose.yml
./swiftdeploy init
./swiftdeploy validate
./swiftdeploy deploy
curl -s http://localhost:8080/metrics | grep "http_requests_total"
curl http://localhost:8080/v1/data 2>&1 | grep -i "404\|refused"
curl http://localhost:8181/health
./swiftdeploy promote canary
./swiftdeploy status
./swiftdeploy audit
```

## Blog Post

Publish the Stage 4B blog post before submission and replace this line with the
published URL.

## GitHub Repository

https://github.com/Collinsthegreat/swiftdeploy
