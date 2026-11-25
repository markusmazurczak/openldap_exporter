#!/usr/bin/env bash
# Container test: builds the image and runs it against a real slapd with the monitor backend.
#   slapd     alpine + openldap, only "database monitor", bind as the monitor rootdn
#   exporter  the image, hardened (read-only, no capabilities, UID 65534)
# Checks the metrics the Grafana dashboard queries, the shell-less health check, a wrong
# password, that the password never appears in the log, and fail-closed on WEB_CFG_FILE.
# Usage: tests/container-test.sh   (rootless Podman; set CONTAINER=docker to use Docker)
set -euo pipefail

DIR="$(cd "$(dirname "$0")/.." && pwd)"
CT="${CONTAINER:-podman}"
IMAGE="localhost/openldap-exporter:test"
ALPINE="docker.io/library/alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf"
TAG="ldapexp-test-$$"
NET="$TAG-net"
PASS="$(head -c 18 /dev/urandom | base64 | tr -d '/+=')"
failures=0

check() { if [ "$2" = "$3" ]; then printf '  ok   %s\n' "$1"; else printf '  FAIL %s (expected %s, got %s)\n' "$1" "$3" "$2"; failures=$((failures + 1)); fi; }
cleanup() {
  "$CT" rm -f -t 0 "$TAG-slapd" "$TAG-exp" "$TAG-bad" "$TAG-web" >/dev/null 2>&1 || true
  "$CT" network rm -f "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# exporter_run NAME PASSWORD [more run arguments] -- hardened like production
exporter_run() {
  local name="$1" pw="$2"; shift 2
  "$CT" run -d --name "$TAG-$name" --network "$NET" --read-only --cap-drop ALL \
    --security-opt no-new-privileges --user 65534:65534 --pids-limit 50 --memory 128m \
    --health-cmd '["/openldap-exporter", "--healthcheck"]' \
    -e LDAP_ADDR="ldap://$TAG-slapd:10389" -e LDAP_USER="cn=admin,cn=Monitor" -e LDAP_PASSWORD="$pw" \
    -e INTERVAL=2s "$@" "$IMAGE" >/dev/null
}
# scrape NAME -> exporter metrics, fetched from the slapd container (busybox wget)
scrape() { "$CT" exec "$TAG-slapd" wget -qO- -T 5 "http://$TAG-$1:9330/metrics" 2>/dev/null || true; }
metric() { awk -v m="$2" 'index($0, m) == 1 {print $NF; exit}' <<<"$1"; }

"$CT" build -q --build-arg GIT_TAG=test -f "$DIR/Containerfile" -t "$IMAGE" "$DIR" >/dev/null
"$CT" network create "$NET" >/dev/null

"$CT" run -d --name "$TAG-slapd" --network "$NET" -e PASS="$PASS" "$ALPINE" sh -c '
  apk add --no-cache -q openldap openldap-back-monitor >/dev/null
  mkdir -p /run/openldap
  cat > /tmp/slapd.conf <<EOF
include /etc/openldap/schema/core.schema
modulepath /usr/lib/openldap
moduleload back_monitor.so
pidfile /run/openldap/slapd.pid
database monitor
rootdn "cn=admin,cn=Monitor"
rootpw $PASS
EOF
  exec slapd -d 0 -f /tmp/slapd.conf -h "ldap://0.0.0.0:10389/"' >/dev/null
for _ in $(seq 1 60); do
  "$CT" exec "$TAG-slapd" sh -c 'nc -z 127.0.0.1 10389' 2>/dev/null && break
  sleep 1
done

exporter_run exp "$PASS"
out=""
for _ in $(seq 1 30); do
  out="$(scrape exp)"
  grep -q '^openldap_scrape{result="ok"}' <<<"$out" && break
  sleep 1
done

echo "Runtime"
check "health check without a shell" "$("$CT" healthcheck run "$TAG-exp" >/dev/null 2>&1 && echo healthy || echo unhealthy)" healthy
check "runs as UID 65534" "$("$CT" inspect --format '{{.Config.User}}' "$TAG-exp")" "65534:65534"
check "no shell in the image" "$("$CT" exec "$TAG-exp" /bin/sh -c true >/dev/null 2>&1 && echo shell || echo none)" none
check "version endpoint" "$("$CT" exec "$TAG-slapd" wget -qO- "http://$TAG-exp:9330/version" 2>/dev/null | cut -d' ' -f1)" test

echo "Metrics against a real slapd"
check "bind ok" "$( [ "$(metric "$out" 'openldap_bind{result="ok"}')" -gt 0 ] 2>/dev/null && echo yes || echo no)" yes
check "scrape ok" "$( [ "$(metric "$out" 'openldap_scrape{result="ok"}')" -gt 0 ] 2>/dev/null && echo yes || echo no)" yes
check "no failed scrape" "$(metric "$out" 'openldap_scrape{result="fail"}')" ""
check "connection counter cn=Total" "$(grep -c '^openldap_monitor_counter_object{dn="cn=Total,cn=Connections,cn=Monitor"}' <<<"$out")" 1
check "operation Bind completed" "$(grep -c '^openldap_monitor_operation{dn="cn=Bind,cn=Operations,cn=Monitor"}' <<<"$out")" 1
check "operation Bind initiated" "$(grep -c '^openldap_monitor_operation_initiated{dn="cn=Bind,cn=Operations,cn=Monitor"}' <<<"$out")" 1
missing=""
# only names from the dashboard's PromQL expressions, not from titles or links
for m in $(grep -oE '"expr": *"[^"]*"' "$DIR/grafana/dashboard.json" | grep -oE 'openldap_[a-z_]+' | sort -u); do
  case "$m" in openldap_monitor_replication*) continue ;; esac  # needs replication
  grep -q "^${m}[{ ]" <<<"$out" || missing="$missing $m"
done
check "all dashboard metrics present" "${missing:-none missing}" "none missing"
check "password not in the log" "$("$CT" logs "$TAG-exp" 2>&1 | grep -cF "$PASS" || true)" 0

echo "Failure cases"
exporter_run bad "wrong"
bad=""
for _ in $(seq 1 20); do bad="$(scrape bad)"; grep -q '^openldap_bind{result="fail"}' <<<"$bad" && break; sleep 1; done
check "wrong password counts as bind failure" "$( [ "$(metric "$bad" 'openldap_bind{result="fail"}')" -gt 0 ] 2>/dev/null && echo yes || echo no)" yes
check "exporter keeps running" "$("$CT" inspect --format '{{.State.Running}}' "$TAG-bad")" true
exporter_run web "$PASS" -e WEB_CFG_FILE=/etc/web.yml
check "WEB_CFG_FILE prevents the start" "$("$CT" wait "$TAG-web" 2>/dev/null | tail -1)" 1

if [ "$failures" -gt 0 ]; then
  echo "--- exporter log"; "$CT" logs "$TAG-exp" 2>&1 | tail -15
  echo "$failures check(s) failed"
  exit 1
fi
echo "All checks passed"
