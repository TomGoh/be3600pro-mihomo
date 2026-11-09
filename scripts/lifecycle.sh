#!/bin/sh
# Sourced by routerctl/refresh. fd 9 is held only across local rule changes.
lifecycle_lock() {
    exec 9> "$RUN/lifecycle.lock"
    flock -x -w 8 9 || { echo LIFECYCLE_BUSY; return 1; }
}
intent_snapshot() {
    EXPECT_EPOCH=${CDX_EXPECT_EPOCH-$(cat "$DATA/intent-generation" 2>/dev/null || true)}
}
intent_current() {
    [ "$EXPECT_EPOCH" = "$(cat "$DATA/intent-generation" 2>/dev/null || true)" ]
}
intent_cancel() {
    next=$(mktemp "$DATA/.intent-XXXXXX") || return 1
    printf '%s-%s\n' "$(date +%s)" "${next##*/}" > "$next"
    chmod 600 "$next"
    mv "$next" "$DATA/intent-generation"
}
