#!/bin/sh
bypass_validate_file() {
    [ -e "$1" ] || return 0
    [ -f "$1" ] && [ -r "$1" ] || return 1
    [ "$(wc -c < "$1")" -le 2048 ] || return 1
    bypass_count=0
    while IFS= read -r bypass_mac || [ -n "$bypass_mac" ]; do
        [ -n "$bypass_mac" ] || continue
        printf '%s\n' "$bypass_mac" | grep -Eq '^[0-9a-fA-F][02468aAcCeE](:[0-9a-fA-F]{2}){5}$' || return 1
        [ "$bypass_mac" != 00:00:00:00:00:00 ] || return 1
        bypass_count=$((bypass_count+1))
        [ "$bypass_count" -le 32 ] || return 1
    done < "$1"
}

bypass_append_returns() {
    bypass_bin=$1
    bypass_table=$2
    bypass_chain=$3
    bypass_file=$4
    bypass_validate_file "$bypass_file" || return 1
    [ -f "$bypass_file" ] || return 0
    while IFS= read -r bypass_mac || [ -n "$bypass_mac" ]; do
        [ -n "$bypass_mac" ] || continue
        "$bypass_bin" -t "$bypass_table" -A "$bypass_chain" -m mac --mac-source "$bypass_mac" -j RETURN || return 1
    done < "$bypass_file"
}
