#!/bin/sh
# No router changes: fetch only the pinned official archive, bounded in RAM/time.
bootstrap_fetch() {
    archive=$1
    attempt=1
    while [ "$attempt" -le 6 ]; do
        printf 'download attempt %s/6\n' "$attempt" > "$RUN/bootstrap-stage"
        for source in "$BASE_URL" "${BASE_FALLBACK_URL:-$BASE_URL}"; do
            # Every source must pass the same archive hash; no third-party mirror.
            rm -f "$archive"
            if curl -4 -fLsS --connect-timeout 8 --max-time 60 --max-filesize 33554432 -H 'Accept: application/octet-stream' -o "$archive" "$source"; then
                actual=$(sha256sum "$archive" | cut -d ' ' -f 1)
                [ "$actual" != "$BASE_ARCHIVE_SHA" ] || return 0
            fi
            rm -f "$archive"
        done
        printf '内核下载或校验失败，正在等待重试；也可用 App 离线恢复\n' > "$RUN/bootstrap-error"
        # An offline upload may have completed while the official sites were down.
        if [ -x /tmp/mihomo ] && [ "$(sha256sum /tmp/mihomo | cut -d ' ' -f 1)" = "$CORE_BINARY_SHA" ]; then return 2; fi
        [ "$attempt" -lt 6 ] || break
        case "$attempt" in 1) delay=5;; 2) delay=15;; 3) delay=30;; 4) delay=60;; *) delay=120;; esac
        printf 'retry in %ss\n' "$delay" > "$RUN/bootstrap-stage"
        sleep "$delay"
        attempt=$((attempt+1))
    done
    rm -f "$archive"
    return 1
}
