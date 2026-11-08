package dev.jose.routerproxy;

/** Main-thread state for one SAF result, including an import interrupted by recreation. */
final class PendingImport {
    private String queued;
    private String active;
    private boolean ready;

    void offer(String uri) { queued = uri; }
    void initialized() { ready = true; }
    String startIfIdle(boolean busy) {
        if (!ready || busy || active != null || queued == null) return null;
        active = queued;
        queued = null;
        return active;
    }
    void complete() { active = null; }
    String snapshot() { return queued != null ? queued : active; }
}
