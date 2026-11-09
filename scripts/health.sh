#!/bin/sh
# Read-only local interception audit. No external network request.
set -u
RUN=${RUN:-/tmp/codex-mihomo}
exec 9> "$RUN/lifecycle.lock"
flock -n -x 9 || { printf '{"state":"busy"}\n'; exit 2; }
family_ok() {
    bin=$1; suffix=$2
    "$bin" -t mangle -C PREROUTING -i br-lan -j "CDX_MH$suffix" 2>/dev/null || return 1
    "$bin" -t nat -C PREROUTING -i br-lan -j "CDX_DNS$suffix" 2>/dev/null || return 1
    "$bin" -t mangle -C "CDX_MH$suffix" -p tcp -m socket --transparent -j TPROXY --on-port 7893 --tproxy-mark 0x6d680000/0xffff0000 2>/dev/null || return 1
    "$bin" -t mangle -C "CDX_MH$suffix" -p tcp --syn -j TPROXY --on-port 7893 --tproxy-mark 0x6d680000/0xffff0000 2>/dev/null || return 1
    "$bin" -t mangle -C "CDX_MH$suffix" -p udp -j TPROXY --on-port 7893 --tproxy-mark 0x6d680000/0xffff0000 2>/dev/null || return 1
    for proto in tcp udp; do
        "$bin" -t nat -C "CDX_DNS$suffix" -p "$proto" --dport 53 -j REDIRECT --to-ports 1053 2>/dev/null || return 1
    done
    if [ "$suffix" = 4 ]; then
        ip rule show | grep -Eq '^10066:.*fwmark 0x6d680000/0xffff0000.*lookup 166' || return 1
        ip route show table 166 | grep -Eq '^local (default|0.0.0.0/0).*dev lo' || return 1
    else
        ip -6 rule show | grep -Eq '^10066:.*fwmark 0x6d680000/0xffff0000.*lookup 166' || return 1
        ip -6 route show table 166 | grep -Eq '^local (default|::/0).*dev lo' || return 1
    fi
}
if [ -f "$RUN/enabled" ]; then
    v4=false; v6=false
    family_ok iptables 4 && v4=true
    family_ok ip6tables 6 && v6=true
    printf '{"state":"checked","expectedEnabled":true,"ipv4":%s,"ipv6":%s}\n' "$v4" "$v6"
    [ "$v4" = true ] && [ "$v6" = true ]
else
    # Stale jumps are unsafe even when the state file claims interception is off.
    clean=true
    for pair in 'iptables 4' 'ip6tables 6'; do
        set -- $pair
        "$1" -t mangle -C PREROUTING -i br-lan -j "CDX_MH$2" 2>/dev/null && clean=false
        "$1" -t nat -C PREROUTING -i br-lan -j "CDX_DNS$2" 2>/dev/null && clean=false
    done
    printf '{"state":"checked","expectedEnabled":false,"inactive":%s}\n' "$clean"
    [ "$clean" = true ]
fi
