# Grafana Dashboard

Pre-built Grafana dashboard for the OpenLDAP Exporter.
Compatible with Grafana 12.x and Prometheus as datasource.

## Panels

| Section | Panels |
|---------|--------|
| **Overview** | Scrape Failures, Current Connections, Active Threads, Max Threads, Uptime, Max File Descriptors |
| **Connections** | Active & Total Connections over time, New Connections rate |
| **Operations** | Completed operations per interval, Initiated operations per interval |
| **Threads** | Thread states over time (Active, Open, Starting, Pending, Backload), Thread Utilization gauge |
| **Statistics** | Bytes sent rate, PDUs / Entries / Referrals rate |
| **Waiters** | Read/Write Waiters, Thread Backload & Pending |
| **Exporter Health** | Dial attempts OK/Fail, Bind attempts OK/Fail, Scrape cycles OK/Fail |

---

## Import via JSON

1. Open your Grafana instance
2. Navigate to **Dashboards → New → Import**
3. Click **Upload dashboard JSON file**
4. Select [`dashboard.json`](./dashboard.json) from this folder
5. In the **Prometheus** datasource dropdown, select your Prometheus instance
6. Click **Import**

---

## Datasource

The dashboard uses a `datasource` template variable. On import, Grafana will ask you to map it to your Prometheus instance.

If your Prometheus datasource is not named `Prometheus`, select the correct one in the import dialog.

---

## Customization tips

- **Scrape interval**: The dashboard uses `$__rate_interval` for rate calculations, which automatically adapts to your Prometheus scrape interval.
- **Thresholds**: The Thread Utilization gauge turns yellow at 70 % and red at 90 %. Adjust via panel edit → Thresholds if your setup has different limits.
- **Auto-refresh**: Set to 30 s by default to match the exporter's default `INTERVAL`.
