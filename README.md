# OpenLDAP Prometheus Exporter

A Prometheus exporter for OpenLDAP 2.6.x that scrapes metrics from the `cn=Monitor` subtree and exposes them via HTTP.

This project is a fork of [tomcz/openldap_exporter](https://github.com/tomcz/openldap_exporter), updated and extended for container-first deployments. The canonical repository for this fork is [markusmazurczak/openldap_exporter](https://github.com/markusmazurczak/openldap_exporter).

Key differences from upstream:

- **OpenLDAP 2.6.x compatible** — tested against OpenLDAP 2.6
- **Environment-variable-only configuration** — no YAML config file support; all options are passed as env vars
- **Container image** — multi-arch Docker image (`linux/amd64`, `linux/arm64`) published to GitHub Container Registry
- **Extended metrics** — `monitorOpInitiated` tracked alongside `monitorOpCompleted` for all LDAP operations
- **Improved diagnostics** — startup check for `cn=Monitor` accessibility with clear error messages

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
| `LDAP_ADDR`            | `--ldapAddr`      | `localhost:389`  | OpenLDAP server address and port                                  |
| `LDAP_USER`            | `--ldapUser`      | *(empty)*        | Bind DN for LDAP authentication (optional)                        |
| `LDAP_PASS`            | `--ldapPass`      | *(empty)*        | Bind password (optional)                                          |
| `INTERVAL`             | `--interval`      | `30s`            | Scrape interval, e.g. `15s`, `1m`                                 |
| `JSON_LOG`             | `--jsonLog`       | `false`          | Set to `true` for JSON-formatted log output                       |
| `WEB_CFG_FILE`         | `--webCfgFile`    | *(empty)*        | Path to a Prometheus web config file for TLS/auth (optional)      |
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

### TLS / web config

Mount a [Prometheus web config file](https://prometheus.io/docs/prometheus/latest/configuration/https/) and reference it via `WEB_CFG_FILE`:

```bash
docker run -d \
  -v /etc/openldap_exporter/web.yml:/etc/web.yml:ro \
  -e WEB_CFG_FILE=/etc/web.yml \
  -e LDAP_ADDR=ldap.example.com:636 \
  ghcr.io/markusmazurczak/openldap_exporter:latest
```

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
docker build \
  --build-arg GIT_TAG=$(git describe --tags 2>/dev/null || echo dev) \
  --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) \
  -t openldap_exporter:local .
```

---

## GitHub Container Registry — automated releases

Container images are built automatically and published to `ghcr.io` using GitHub Actions.

### Prerequisites

Enable write permissions for GitHub Actions in your repository:
**Settings → Actions → General → Workflow permissions → Read and write permissions**

### Creating a release

Push a tag in the format `vX.Y.Z` to trigger a build:

```bash
git tag v2.1.0
git push origin v2.1.0
```

GitHub Actions will build and push:

| Tag | Example |
|-----|---------|
| Full version | `ghcr.io/markusmazurczak/openldap_exporter:2.1.0` |
| Minor version | `ghcr.io/markusmazurczak/openldap_exporter:2.1` |
| Major version | `ghcr.io/markusmazurczak/openldap_exporter:2` |

Both `linux/amd64` and `linux/arm64` platforms are built.

### Manual trigger

A build can also be triggered manually via **Actions → Build and Push Docker Image → Run workflow** with a custom tag name (e.g. `dev` or `test`).

### Making the image public

After the first build the package is private by default. To make it public:

1. Go to `https://github.com/markusmazurczak?tab=packages`
2. Select the `openldap_exporter` package
3. **Package settings → Change visibility → Public**

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

1. Install [Go 1.22+](https://golang.org/)
2. Clone this repository
3. Build:

```bash
make build
```

The binary is placed in `target/openldap_exporter`.

---

## License

See [LICENSE.md](LICENSE.md).

---

*This fork: [markusmazurczak/openldap_exporter](https://github.com/markusmazurczak/openldap_exporter) — forked from [tomcz/openldap_exporter](https://github.com/tomcz/openldap_exporter).*
