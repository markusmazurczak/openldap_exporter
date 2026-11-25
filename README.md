# OpenLDAP Prometheus Exporter

A Prometheus exporter for OpenLDAP 2.6.x that scrapes metrics from the `cn=Monitor` subtree and exposes them via HTTP.

This project is a fork of [tomcz/openldap_exporter](https://github.com/tomcz/openldap_exporter), updated and extended for container-first deployments. The canonical repository for this fork is [markusmazurczak/openldap_exporter](https://github.com/markusmazurczak/openldap_exporter).

Key differences from upstream:

- **OpenLDAP 2.6.x compatible** — tested against OpenLDAP 2.6
- **Environment-variable-only configuration** — no YAML config file support; all options are passed as env vars
- **Container image** — multi-arch Docker image (`linux/amd64`, `linux/arm64`) published to GitHub Container Registry
- **Extended metrics** — `monitorOpInitiated` tracked alongside `monitorOpCompleted` for all LDAP operations
- **Improved diagnostics** — startup check for `cn=Monitor` accessibility with clear error messages
- **Minimal, hardened image** — static binary on `scratch` (no shell, no package manager), runs as UID 65534, current Go toolchain
- **Built-in health check** — `openldap-exporter --healthcheck` probes the local metrics endpoint, so container health checks need no shell or `wget`
- **Maintained dependencies** — `github.com/go-ldap/ldap/v3` (replaces the archived `gopkg.in/ldap.v2`), timeouts for LDAP and HTTP, TLS 1.2+ with server name verification for `ldaps://`

---

## Quick Start

```bash
docker run -d \
  -p 9330:9330 \
  -e LDAP_ADDR=ldap.example.com:389 \
  -e LDAP_USER=cn=monitoring,cn=Monitor \
  -e LDAP_PASS=secret \
  ghcr.io/markusmazurczak/openldap_exporter:latest
```

Metrics are available at `http://localhost:9330/metrics`.

---

## Setting up OpenLDAP for monitoring

`slapd` supports an optional LDAP monitoring interface you can use to obtain information about the current state of your instance. Documentation can be found in the OpenLDAP [backend guide](https://www.openldap.org/doc/admin26/backends.html#Monitor) and [administration guide](http://www.openldap.org/doc/admin26/monitoringslapd.html).

Add the following to the bottom of your `slapd.conf`:

```
database monitor
rootdn "cn=monitoring,cn=Monitor"
rootpw YOUR_MONITORING_ROOT_PASSWORD
```

If your `slapd` installation loads backends as modules, also add:

```
moduleload back_monitor
```

---

## Configuration

All configuration is done exclusively via environment variables or command-line flags. There is no configuration file.

| Environment Variable   | Flag              | Default          | Description                                                       |
|------------------------|-------------------|------------------|-------------------------------------------------------------------|
| `PROM_ADDR`            | `--promAddr`      | `:9330`          | Bind address for the Prometheus HTTP metrics server               |
| `METRICS_PATH`         | `--metrPath`      | `/metrics`       | HTTP path to expose metrics on                                    |
| `LDAP_NET`             | `--ldapNet`       | `tcp`            | Network type: `tcp` or `unix`                                     |
| `LDAP_ADDR`            | `--ldapAddr`      | `localhost:389`  | `host:port`, or an `ldap://`, `ldaps://` or `ldapi://` URL        |
| `LDAP_USER`            | `--ldapUser`      | *(empty)*        | Bind DN for LDAP authentication (optional)                        |
| `LDAP_PASS`            | `--ldapPass`      | *(empty)*        | Bind password (optional)                                          |
| `INTERVAL`             | `--interval`      | `30s`            | Scrape interval, e.g. `15s`, `1m`                                 |
| `JSON_LOG`             | `--jsonLog`       | `false`          | Set to `true` for JSON-formatted log output                       |
| `REPLICATION_OBJECTS`  | `--replicationObject` | *(empty)*   | LDAP DNs to monitor for replication — see note below             |

### Replication monitoring

Since LDAP DNs contain commas, the `REPLICATION_OBJECTS` environment variable uses `|` (pipe) as the separator between DNs:

```bash
-e REPLICATION_OBJECTS="dc=example,dc=com|dc=other,dc=org"
```

When using the CLI flag directly, pass it multiple times:

```bash
openldap_exporter \
  --replicationObject "dc=example,dc=com" \
  --replicationObject "dc=other,dc=org"
```

### Metrics endpoint security

The metrics endpoint serves plain HTTP. Run the exporter on an internal network (for example
the network Prometheus scrapes from) or behind a reverse proxy that adds TLS and
authentication.

Support for a Prometheus web config file (`WEB_CFG_FILE`) was removed in November 2026 together
with the `exporter-toolkit` dependency. If `WEB_CFG_FILE` is still set, the exporter refuses to
start rather than serving metrics without the protection the file was meant to add.

To reach OpenLDAP over TLS, use an `ldaps://` address, e.g. `LDAP_ADDR=ldaps://ldap.example.com:636`.

### Health check

```bash
openldap-exporter --healthcheck   # exit 0 if the local metrics endpoint answers with 200
```

It reads the same `PROM_ADDR` and `METRICS_PATH` as the server. Example for Podman/Docker:
`--health-cmd '["/openldap-exporter", "--healthcheck"]'`.

---

## Docker

### Docker Compose example

```yaml
services:
  openldap-exporter:
    image: ghcr.io/markusmazurczak/openldap_exporter:latest
    restart: unless-stopped
    ports:
      - "9330:9330"
    environment:
      LDAP_ADDR: ldap:389
      LDAP_USER: cn=monitoring,cn=Monitor
      LDAP_PASS: secret
      INTERVAL: 30s
      JSON_LOG: "true"
```

### Build locally

```bash
docker build -f Containerfile \
  --build-arg GIT_TAG=$(git describe --tags 2>/dev/null || echo dev) \
  --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) \
  -t openldap_exporter:local .
```

---

## Metrics

### Collected metrics

| Metric | Description |
|--------|-------------|
| `openldap_monitor_counter_object{dn}` | Counters from `monitorCounterObject` entries (Connections, Statistics, Waiters) |
| `openldap_monitored_object{dn}` | Numeric values from `monitoredObject` entries (Threads, Time, Listeners) |
| `openldap_monitor_operation{dn}` | Completed LDAP operations (`monitorOpCompleted`) per operation type |
| `openldap_monitor_operation_initiated{dn}` | Initiated LDAP operations (`monitorOpInitiated`) per operation type |
| `openldap_monitor_replication{id,type}` | Replication timestamps and counters (when configured) |
| `openldap_bind{result}` | Successful / failed LDAP bind attempts (`ok` or `fail`) |
| `openldap_dial{result}` | Successful / failed LDAP dial attempts (`ok` or `fail`) |
| `openldap_scrape{result}` | Successful / failed scrape cycles (`ok` or `fail`) |

### Coverage of cn=Monitor

All numeric entries under `cn=Monitor` are scraped automatically via object-class-based LDAP searches:

- **Connections** — `cn=Total`, `cn=Current`, `cn=Max File Descriptors`
- **Statistics** — `cn=Bytes`, `cn=PDU`, `cn=Entries`, `cn=Referrals`
- **Waiters** — `cn=Read`, `cn=Write`
- **Threads** — `cn=Max`, `cn=Max Pending`, `cn=Open`, `cn=Starting`, `cn=Active`, `cn=Pending`, `cn=Backload`
- **Time** — `cn=Start`, `cn=Current`, `cn=Uptime`
- **Operations** — Bind, Unbind, Search, Compare, Modify, Modrdn, Add, Delete, Abandon, Extended
- **Backends, Databases, Listeners, Overlays** — numeric attributes where available

String-valued entries (e.g. `cn=State,cn=Threads,cn=Monitor` with value `running`) are silently ignored.

### Example output

```
# HELP openldap_monitor_counter_object cn=Monitor (objectClass=monitorCounterObject) monitorCounter
# TYPE openldap_monitor_counter_object gauge
openldap_monitor_counter_object{dn="cn=Bytes,cn=Statistics,cn=Monitor"} 1.857812777e+09
openldap_monitor_counter_object{dn="cn=Current,cn=Connections,cn=Monitor"} 50
openldap_monitor_counter_object{dn="cn=Entries,cn=Statistics,cn=Monitor"} 4.226632e+06
openldap_monitor_counter_object{dn="cn=Max File Descriptors,cn=Connections,cn=Monitor"} 1024
openldap_monitor_counter_object{dn="cn=PDU,cn=Statistics,cn=Monitor"} 4.446117e+06
openldap_monitor_counter_object{dn="cn=Read,cn=Waiters,cn=Monitor"} 31
openldap_monitor_counter_object{dn="cn=Referrals,cn=Statistics,cn=Monitor"} 0
openldap_monitor_counter_object{dn="cn=Total,cn=Connections,cn=Monitor"} 65383
openldap_monitor_counter_object{dn="cn=Write,cn=Waiters,cn=Monitor"} 0

# HELP openldap_monitor_operation cn=Operations,cn=Monitor (objectClass=monitorOperation) monitorOpCompleted
# TYPE openldap_monitor_operation gauge
openldap_monitor_operation{dn="cn=Abandon,cn=Operations,cn=Monitor"} 0
openldap_monitor_operation{dn="cn=Add,cn=Operations,cn=Monitor"} 0
openldap_monitor_operation{dn="cn=Bind,cn=Operations,cn=Monitor"} 57698
openldap_monitor_operation{dn="cn=Search,cn=Operations,cn=Monitor"} 161789
openldap_monitor_operation{dn="cn=Unbind,cn=Operations,cn=Monitor"} 9336

# HELP openldap_monitor_operation_initiated cn=Operations,cn=Monitor (objectClass=monitorOperation) monitorOpInitiated
# TYPE openldap_monitor_operation_initiated gauge
openldap_monitor_operation_initiated{dn="cn=Bind,cn=Operations,cn=Monitor"} 57700
openldap_monitor_operation_initiated{dn="cn=Search,cn=Operations,cn=Monitor"} 161792

# HELP openldap_monitored_object cn=Monitor (objectClass=monitoredObject) monitoredInfo
# TYPE openldap_monitored_object gauge
openldap_monitored_object{dn="cn=Active,cn=Threads,cn=Monitor"} 1
openldap_monitored_object{dn="cn=Max,cn=Threads,cn=Monitor"} 16
openldap_monitored_object{dn="cn=Open,cn=Threads,cn=Monitor"} 8
openldap_monitored_object{dn="cn=Uptime,cn=Time,cn=Monitor"} 1.225737e+06
```

---

## Grafana Dashboard

A pre-built Grafana dashboard is included in the [`grafana/`](grafana/) directory.

It covers all exporter metrics — connections, operations, threads, statistics, waiters, and exporter health — and is compatible with Grafana 12.x.

**Import:** Dashboards → New → Import → Upload JSON file → select [`grafana/dashboard.json`](grafana/dashboard.json)

See [`grafana/README.md`](grafana/README.md) for full import instructions, including how to publish the dashboard to grafana.com to get an importable ID.

---

## Building from source

1. Install Go (the version in [`go.mod`](go.mod) or newer)
2. Clone this repository
3. Test and build:

```bash
make build      # runs go vet and go test first
```

The binary is placed in `target/openldap_exporter`.

---

## Development

```bash
make test                   # go vet + go test
golangci-lint run           # v2, same version as in CI
tests/container-test.sh     # builds the image and checks it against a real slapd (Podman)
```

CI (`.github/workflows/build.yml`) runs lint, tests, `govulncheck`, the build and the container
test on every push and pull request, and weekly on a schedule so new vulnerabilities surface
without a commit. Dependabot keeps Go modules, the builder image and the actions current.
Actions are pinned by commit SHA.

## Releases

A release is a tag `vMAJOR.MINOR.PATCH` on `master` with a GitHub release and the image
`ghcr.io/markusmazurczak/openldap_exporter:<tag>` (also `:latest`), built from `Containerfile`
for `linux/amd64` and `linux/arm64`.

Changes reach `master` through pull requests; the required checks are the `build` and
`container-test` jobs. Every merged pull request is released automatically by
`.github/workflows/release.yml`. The bump follows the pull request:

| Pull request                                            | Bump       |
|---------------------------------------------------------|------------|
| label `release:skip`                                    | no release |
| label `release:major`, `release:minor`, `release:patch` | that bump  |
| title `type!: …` / `type(scope)!: …`                    | major      |
| title `feat: …` / `feat(scope): …`                      | minor      |
| anything else (`fix:`, `chore(deps):`, …)               | patch      |

When the weekly `govulncheck` run reports a finding that a newer Go or module version fixes,
the update goes through a pull request like any other change. A tag pushed by hand
(`git tag -a vX.Y.Z -m … && git push origin vX.Y.Z`) still builds the image through
`.github/workflows/docker.yml`.

Metric names and labels are part of the interface (dashboards, alerts): renaming one is a
breaking change.

---

## License

See [LICENSE.md](LICENSE.md).

---

*This fork: [markusmazurczak/openldap_exporter](https://github.com/markusmazurczak/openldap_exporter) — forked from [tomcz/openldap_exporter](https://github.com/tomcz/openldap_exporter).*
