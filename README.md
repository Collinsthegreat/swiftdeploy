# SwiftDeploy - Declarative Container Stack Manager

SwiftDeploy is a CLI tool that generates Nginx and Docker Compose configurations
from a single declarative `manifest.yaml` and manages the full container lifecycle.

## Prerequisites

- Docker 24+
- Docker Compose v2+
- Python 3.8+
- `pyyaml` and `jinja2` Python packages

```bash
pip install pyyaml jinja2
chmod +x swiftdeploy
```

## Quick Start (Fresh Machine)

```bash
# 1. Clone the repository
git clone https://github.com/Collinsthegreat/swiftdeploy.git
cd swiftdeploy

# 2. Install CLI Python dependencies
pip install pyyaml jinja2

# 3. Make CLI executable
chmod +x swiftdeploy

# 4. Build the API image
docker build -t swift-deploy-1-node:latest app/

# 5. Generate configs from manifest
./swiftdeploy init

# 6. Run pre-flight checks
./swiftdeploy validate

# 7. Deploy the stack
./swiftdeploy deploy
```

## Subcommand Reference

### `./swiftdeploy init`
Parses `manifest.yaml` and generates `nginx.conf` and `docker-compose.yml`
from templates. These files are never committed - always regenerated.

### `./swiftdeploy validate`
Runs 5 pre-flight checks:
1. `manifest.yaml` exists and is valid YAML
2. All required fields are present and non-empty
3. Docker image exists locally
4. Nginx port is not already bound on host
5. Generated `nginx.conf` syntax is valid

Exits non-zero on any failure with clear PASS/FAIL output.

### `./swiftdeploy deploy`
Runs `init`, brings up the full stack with `docker compose up -d`,
then blocks until `/healthz` returns 200 or 60 seconds elapse.

### `./swiftdeploy promote [canary|stable]`
Switches deployment mode:
1. Updates `mode` field in `manifest.yaml` in-place
2. Regenerates `docker-compose.yml` with new `MODE` env var
3. Restarts the service container only (`--force-recreate --no-deps`)
4. Confirms new mode by polling `/healthz` until `mode` field matches

### `./swiftdeploy teardown [--clean]`
Removes all containers, networks, and volumes.
`--clean` also deletes generated `nginx.conf` and `docker-compose.yml`.

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/` | Welcome message, mode, version, timestamp |
| GET | `/healthz` | Status + uptime in seconds |
| POST | `/chaos` | Simulate degraded behavior (canary only) |

## Architecture

```
Client -> Nginx (:8080) -> swiftdeploy-app (:3000, internal only)
manifest.yaml -> swiftdeploy init -> nginx.conf + docker-compose.yml
```

## GitHub Repository

https://github.com/Collinsthegreat/swiftdeploy
