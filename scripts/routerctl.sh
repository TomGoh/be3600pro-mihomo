#!/bin/sh
set -eu
DATA=/data/codex-router
RUN=/tmp/codex-mihomo
LAN=br-lan
MARK=0x6d680000/0xffff0000
TABLE=166
PREF=10066
mkdir -p "$RUN"
umask 077
. "$DATA/lifecycle.sh"
intent_snapshot

ensure_xtables() {
    directory="$RUN/xtables-global"
    mkdir -p "$directory"
    for extension in TPROXY socket; do
        [ -s "$DATA/libxt_$extension.so" ] || { echo XTABLES_EXTENSION_MISSING; return 1; }
    done
    for library in /usr/lib/iptables/*.so; do
        [ "$library" -ef "$directory/${library##*/}" ] || cp -L "$library" "$directory/${library##*/}"
    done
    for extension in TPROXY socket; do
        cp "$DATA/libxt_$extension.so" "$directory/libxt_$extension.so"
    done
    chmod 755 "$directory" "$directory"/*.so
    [ "$directory" -ef /usr/lib/iptables ] || mount -o bind "$directory" /usr/lib/iptables
    for extension in TPROXY socket; do
        expected=$(sha256sum "$DATA/libxt_$extension.so" | cut -d ' ' -f 1)
        actual=$(sha256sum "/usr/lib/iptables/libxt_$extension.so" | cut -d ' ' -f 1)
        [ "$actual" = "$expected" ] || { echo XTABLES_EXTENSION_MISMATCH; return 1; }
    done
}

bridge_prepare() {
    for family in iptables ip6tables; do
        file="/proc/sys/net/bridge/bridge-nf-call-$family"
        backup="$DATA/backup/bridge-nf-$family"
        [ -f "$file" ] || continue
        if [ ! -f "$backup" ]; then cat "$file" > "$backup"; fi
        printf '0\n' > "$file"
    done
}

bridge_restore() {
    for family in iptables ip6tables; do
        file="/proc/sys/net/bridge/bridge-nf-call-$family"
        backup="$DATA/backup/bridge-nf-$family"
        if [ -f "$file" ] && [ -f "$backup" ]; then
            value=$(cat "$backup")
            case "$value" in 0|1) printf '%s\n' "$value" > "$file" ;; esac
        fi
    done
}

desired() {
    printf '%s\n' "$1" > "$DATA/desired-enabled.next" || return 1
    chmod 600 "$DATA/desired-enabled.next" || return 1
    mv "$DATA/desired-enabled.next" "$DATA/desired-enabled" || return 1
}

core_ready() {
    curl -f -K "$DATA/core-curl.conf" --max-time 2 http://127.0.0.1:9090/version >/dev/null 2>&1
}

self_test() {
    mode=$(jsonfilter -i "$DATA/config.json" -e '@.mode') || return 1
    case "$mode" in
        direct) url=https://www.baidu.com/robots.txt; expected=200 ;;
        rule|global) url=https://www.google.com/generate_204; expected=204 ;;
        *) return 1 ;;
    esac
    code=$(curl -x http://127.0.0.1:7890 --connect-timeout 4 --max-time 12 -sS -o /dev/null -w '%{http_code}' "$url") || return 1
    [ "$code" = "$expected" ]
}

ensure_cache() {
    if [ ! -d "$RUN/rule-cache" ]; then
        expected=$(cat "$DATA/initial-rules.sha256")
        actual=$(sha256sum "$DATA/initial-rules.tgz" | cut -d ' ' -f 1)
        [ "$actual" = "$expected" ] || { echo RULE_CACHE_CHECKSUM_FAILED; return 1; }
        tar -xzf "$DATA/initial-rules.tgz" -C "$RUN"
    fi
    mkdir -p "$RUN/providers"
    for archive in "$DATA"/subcache/*.gz; do
        [ -f "$archive" ] || continue
        name=${archive##*/}
        case "$name" in *[!a-zA-Z0-9_.-]*) return 1 ;; esac
        target="$RUN/providers/${name%.gz}"
        if [ ! -f "$target" ]; then gzip -dc "$archive" > "$target.next"; mv "$target.next" "$target"; fi
    done
}

remove_family() {
    bin=$1
    suffix=$2
    while "$bin" -t mangle -C PREROUTING -i "$LAN" -j "CDX_MH$suffix" 2>/dev/null; do
        "$bin" -t mangle -D PREROUTING -i "$LAN" -j "CDX_MH$suffix" || break
    done
    while "$bin" -t nat -C PREROUTING -i "$LAN" -j "CDX_DNS$suffix" 2>/dev/null; do
        "$bin" -t nat -D PREROUTING -i "$LAN" -j "CDX_DNS$suffix" || break
    done
    "$bin" -t mangle -F "CDX_MH$suffix" 2>/dev/null || true
    "$bin" -t mangle -X "CDX_MH$suffix" 2>/dev/null || true
    "$bin" -t nat -F "CDX_DNS$suffix" 2>/dev/null || true
    "$bin" -t nat -X "CDX_DNS$suffix" 2>/dev/null || true
}

disable_runtime() {
    remove_family iptables 4
    remove_family ip6tables 6
    ip rule del pref "$PREF" fwmark "$MARK" lookup "$TABLE" 2>/dev/null || true
    ip -6 rule del pref "$PREF" fwmark "$MARK" lookup "$TABLE" 2>/dev/null || true
    ip route del local 0.0.0.0/0 dev lo table "$TABLE" 2>/dev/null || true
    ip -6 route del local ::/0 dev lo table "$TABLE" 2>/dev/null || true
    bridge_restore || true
    ipset destroy CDX_RLY4 2>/dev/null || true
    ipset destroy CDX_RLY6 2>/dev/null || true
    rm -f "$RUN/enabled" "$RUN/scope" "$RUN/pending-token" "$RUN/confirmed-token" "$RUN/bypass-active"
}

install_acl_family() {
    bin=$1
    chain=$2
    "$bin" -t filter -N "$chain" 2>/dev/null || true
    "$bin" -t filter -F "$chain"
    "$bin" -t filter -A "$chain" -i lo -j RETURN
    "$bin" -t filter -A "$chain" -i "$LAN" -j RETURN
    "$bin" -t filter -A "$chain" -p tcp -m multiport --dports 1053,2222,7890,7892,7893,9090,9443 -j DROP
    "$bin" -t filter -A "$chain" -p udp -m multiport --dports 1053,7890,7893 -j DROP
    "$bin" -t filter -C INPUT -j "$chain" 2>/dev/null || "$bin" -t filter -I INPUT 1 -j "$chain"
}

install_acl() {
    install_acl_family iptables CDX_ACCESS4
    install_acl_family ip6tables CDX_ACCESS6
}

install_family() {
    bin=$1
    suffix=$2
    scope=$3
    "$bin" -t mangle -N "CDX_MH$suffix"
    "$bin" -t nat -N "CDX_DNS$suffix"
    if [ -n "$scope" ]; then
        "$bin" -t mangle -A "CDX_MH$suffix" -m mac ! --mac-source "$scope" -j RETURN
        "$bin" -t nat -A "CDX_DNS$suffix" -m mac ! --mac-source "$scope" -j RETURN
    fi
    bypass_append_returns "$bin" mangle "CDX_MH$suffix" "$BYPASS_POLICY"
    bypass_append_returns "$bin" nat "CDX_DNS$suffix" "$BYPASS_POLICY"
    "$bin" -t mangle -A "CDX_MH$suffix" -p udp --dport 53 -j RETURN
    "$bin" -t mangle -A "CDX_MH$suffix" -p tcp --dport 53 -j RETURN
    if [ "$suffix" = 4 ]; then
        destinations='0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16 224.0.0.0/4 240.0.0.0/4'
    else
        destinations='::/128 ::1/128 ::ffff:0:0/96 fc00::/7 fe80::/10 ff00::/8'
    fi
    for destination in $destinations; do "$bin" -t mangle -A "CDX_MH$suffix" -d "$destination" -j RETURN; done
    for protocol in tcp udp; do
        "$bin" -t mangle -A "CDX_MH$suffix" -p "$protocol" -m set --match-set "CDX_RLY$suffix" dst,dst -j RETURN
        "$bin" -t nat -A "CDX_DNS$suffix" -p "$protocol" -m set --match-set "CDX_RLY$suffix" dst,dst -j RETURN
    done
    # Preserve native TCP sessions established before activation. Established
    # transparent sockets still receive their packets; only new SYNs are captured.
    "$bin" -t mangle -A "CDX_MH$suffix" -p tcp -m socket --transparent -j TPROXY --on-port 7893 --tproxy-mark "$MARK"
    "$bin" -t mangle -A "CDX_MH$suffix" -p tcp --syn -j TPROXY --on-port 7893 --tproxy-mark "$MARK"
    "$bin" -t mangle -A "CDX_MH$suffix" -p udp -j TPROXY --on-port 7893 --tproxy-mark "$MARK"
    "$bin" -t nat -A "CDX_DNS$suffix" -p udp --dport 53 -j REDIRECT --to-ports 1053
    "$bin" -t nat -A "CDX_DNS$suffix" -p tcp --dport 53 -j REDIRECT --to-ports 1053
    "$bin" -t mangle -I PREROUTING 1 -i "$LAN" -j "CDX_MH$suffix"
    "$bin" -t nat -I PREROUTING 1 -i "$LAN" -j "CDX_DNS$suffix"
}

enable_pending() {
    # Load only on activation: missing optional policy helpers must never block teardown.
    . "$DATA/bypass-rules.sh"
    BYPASS_POLICY="$RUN/bypass-next"
    if [ -f "$DATA/bypass-macs" ]; then cp "$DATA/bypass-macs" "$BYPASS_POLICY"; else : > "$BYPASS_POLICY"; fi
    bypass_validate_file "$BYPASS_POLICY" || { echo INVALID_BYPASS_POLICY; return 1; }
    scope=${1:-}
    if [ -n "$scope" ]; then
        printf '%s' "$scope" | grep -Eq '^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$' || { echo INVALID_TEST_MAC; return 1; }
    fi
    [ -x "$RUN/router-agent" ] || { echo RELAY_PLANNER_MISSING; return 1; }
    exec 6> "$RUN/relay-plan.lock"
    flock -x -w 12 6 || return 1
    "$RUN/router-agent" -root "$DATA" -run "$RUN" -write-relay-bypass
    flock -u 6
    exec 6>&-
    core_ready || { echo CORE_NOT_READY; return 1; }
    self_test || { echo PROXY_TEST_FAILED; return 1; }
    lifecycle_lock || return 1
    intent_current || { echo OPERATION_CANCELLED; return 1; }
    disable_runtime
    token="$(date +%s)-$$"
    printf '%s\n' "$token" > "$RUN/pending-token"
    sh /etc/rc.common "$DATA/rollback.init" restart 9>&-
    trap 'disable_runtime' EXIT
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM
    ipset restore -exist < "$RUN/relay-endpoints.ipset"
    bridge_prepare
    install_acl
    ip route replace local 0.0.0.0/0 dev lo table "$TABLE"
    ip -6 route replace local ::/0 dev lo table "$TABLE"
    ip rule add pref "$PREF" fwmark "$MARK" lookup "$TABLE"
    ip -6 rule add pref "$PREF" fwmark "$MARK" lookup "$TABLE"
    install_family iptables 4 "$scope"
    install_family ip6tables 6 "$scope"
    mv "$BYPASS_POLICY" "$RUN/bypass-active"
    printf '%s' "$scope" > "$RUN/scope"
    printf '1\n' > "$RUN/enabled"
    trap - EXIT HUP INT TERM
    printf '%s\n' "$token"
}

relay_refresh() {
    [ -f "$RUN/enabled" ] || return 0
    exec 6> "$RUN/relay-plan.lock"
    flock -n -x 6 || return 0
    "$RUN/router-agent" -root "$DATA" -run "$RUN" -write-relay-bypass || return 1
    lifecycle_lock || return 1
    [ -f "$RUN/enabled" ] && intent_current || return 0
    # Populate private sets before the live names are changed. Never flush live sets.
    sed -e 's/CDX_RLY4/CDX_RLY4N/g' -e 's/CDX_RLY6/CDX_RLY6N/g' "$RUN/relay-endpoints.ipset" > "$RUN/relay-refresh.ipset"
    trap 'ipset destroy CDX_RLY4N 2>/dev/null || true; ipset destroy CDX_RLY6N 2>/dev/null || true' EXIT
    ipset restore -exist < "$RUN/relay-refresh.ipset" || return 1
    ipset swap CDX_RLY4N CDX_RLY4 || return 1
    if ! ipset swap CDX_RLY6N CDX_RLY6; then
        ipset swap CDX_RLY4N CDX_RLY4 || true
        return 1
    fi
    date +%s > "$RUN/relay-applied-at"
}

confirm_pending() {
    intent_current || return 1
    token=$(cat "$RUN/pending-token") || return 1
    [ -n "$token" ] || return 1
    [ -f "$RUN/enabled" ] || return 1
    if [ ! -s "$RUN/scope" ]; then desired 1 || return 1; fi
    printf '%s\n' "$token" > "$RUN/confirmed-token" || return 1
    sh /etc/rc.common "$DATA/rollback.init" stop 9>&- || return 1
}

case "${1:-status}" in
    enable|enable-pending|acl) ensure_xtables ;;
esac

case "${1:-status}" in
    acl) install_acl ;;
    core-start)
        ensure_cache
        [ -x /tmp/mihomo ] || { echo CORE_MISSING; exit 1; }
        . "$DATA/core-release.sh"
        [ "$(sha256sum /tmp/mihomo | cut -d ' ' -f 1)" = "$CORE_BINARY_SHA" ] || { echo CORE_CHECKSUM_MISMATCH; exit 1; }
        /tmp/mihomo -t -d "$RUN" -f "$DATA/config.json" > "$RUN/validate.log" 2>&1
        sh /etc/rc.common "$DATA/core.init" start
        count=0
        until core_ready; do count=$((count+1)); [ "$count" -lt 25 ] || exit 1; sleep 1; done
        if [ -x "$RUN/router-agent" ]; then "$RUN/router-agent" -restore-selections; fi
        ;;
    core-stop) lifecycle_lock; disable_runtime; sh /etc/rc.common "$DATA/core.init" stop ;;
    enable-pending) enable_pending "${2:-}" ;;
    confirm) lifecycle_lock; if ! confirm_pending; then disable_runtime; desired 0; exit 1; fi ;;
    enable) enable_pending ''; if ! confirm_pending; then disable_runtime; desired 0; exit 1; fi ;;
    disable) intent_cancel; desired 0; lifecycle_lock; disable_runtime; desired 0 ;;
    disable-runtime) lifecycle_lock; disable_runtime ;;
    bootstrap-retry) sh /etc/rc.common "$DATA/bootstrap.init" restart ;;
    relay-refresh) relay_refresh ;;
    health) sh "$DATA/health.sh" ;;
    status)
        if [ -f "$RUN/enabled" ]; then enabled=true; else enabled=false; fi
        if [ -s "$RUN/scope" ]; then scope=test-device; else scope=lan; fi
        printf '{"enabled":%s,"scope":"%s"}\n' "$enabled" "$scope"
        ;;
    *) echo UNKNOWN_ACTION; exit 2 ;;
esac
