package dev.jose.routerproxy;

public final class PendingImportTest {
    private static int count;
    private static void equal(Object actual, Object expected) {
        if (!java.util.Objects.equals(actual, expected)) throw new AssertionError("Import state mismatch");
        count++;
    }
    public static void main(String[] args) {
        String file = "content://test.documents/document/pairing.json";
        PendingImport state = new PendingImport();
        state.offer(file); // SAF result before asynchronous startup completes.
        equal(state.startIfIdle(false), null);
        equal(state.snapshot(), file);
        state.initialized();
        equal(state.startIfIdle(true), null); // Startup/another request is still busy.
        equal(state.startIfIdle(false), file);
        equal(state.startIfIdle(false), null); // Never duplicate an active import.
        equal(state.snapshot(), file); // Preserve in-flight input during recreation.
        PendingImport recreated = new PendingImport();
        recreated.offer(state.snapshot());
        equal(recreated.startIfIdle(false), null);
        recreated.initialized();
        equal(recreated.startIfIdle(false), file);
        recreated.complete();
        equal(recreated.snapshot(), null);
        equal(recreated.startIfIdle(false), null);
        state.complete();
        equal(state.snapshot(), null);
        state.offer(file);
        equal(state.startIfIdle(true), null);
        state.complete(); // Unrelated/previous completion must not clear a queued result.
        equal(state.snapshot(), file);
        equal(state.startIfIdle(false), file);
        state.complete(); // Failure also consumes its active result, without looping retries.
        equal(state.startIfIdle(false), null);
        System.out.println("PASS: " + count + " pending-import lifecycle checks");
    }
}
