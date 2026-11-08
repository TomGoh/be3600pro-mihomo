#!/bin/sh
# Run on RN01 over SSH. Replaces only the management agent, not Mihomo or routes.
set -eu
umask 077
DATA=/data/codex-router
RUN=/tmp/codex-mihomo
archive=${1:?Usage: update-agent.sh archive.gz archive-sha256}
expected=${2:?Missing archive SHA-256}
printf '%s' "$expected" | grep -Eq '^[0-9a-f]{64}$' || exit 2
[ "$(sha256sum "$archive" | cut -d ' ' -f 1)" = "$expected" ] || { echo ARCHIVE_CHECKSUM_MISMATCH; exit 1; }
stage=$(mktemp -d /tmp/cdx-agent-update.XXXXXX)
committed=0
stopped=0
cleanup() {
    if [ "$committed" != 1 ] && [ "$stopped" = 1 ]; then
        sh /etc/rc.common "$DATA/agent.init" stop || true
        cp "$stage/agent.old" "$RUN/router-agent"
        chmod 700 "$RUN/router-agent"
        cp "$stage/archive.old" "$DATA/router-agent.gz"
        cp "$stage/hash.old" "$DATA/agent.sha256"
        sh /etc/rc.common "$DATA/agent.init" start || true
    fi
    rm -rf "$stage"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
cp "$RUN/router-agent" "$stage/agent.old"
cp "$DATA/router-agent.gz" "$stage/archive.old"
cp "$DATA/agent.sha256" "$stage/hash.old"
gzip -dc "$archive" > "$stage/agent.new"
chmod 700 "$stage/agent.new"
config_before=$(sha256sum "$DATA/config.json" | cut -d ' ' -f 1)
stopped=1
sh /etc/rc.common "$DATA/agent.init" stop
mv "$stage/agent.new" "$RUN/router-agent"
# Flash cannot hold two compressed agents; a verified rollback copy is in RAM.
cp "$archive" "$DATA/router-agent.gz"
printf '%s\n' "$expected" > "$DATA/agent.sha256"
chmod 600 "$DATA/router-agent.gz" "$DATA/agent.sha256"
sh /etc/rc.common "$DATA/agent.init" start
count=0
until curl -f -K "$DATA/agent-curl.conf" --max-time 2 https://192.168.31.1:9443/api/status >/dev/null 2>&1; do
    count=$((count+1)); [ "$count" -lt 12 ] || exit 1; sleep 1
done
[ "$(sha256sum "$DATA/config.json" | cut -d ' ' -f 1)" = "$config_before" ] || exit 1
committed=1
echo AGENT_UPDATE_OK_CONFIG_UNCHANGED
