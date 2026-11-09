package dev.jose.routerproxy;
import java.util.concurrent.atomic.AtomicInteger;
/** Cancellation fence also checked when queued executor work begins. */
final class TaskEpoch {
 private final AtomicInteger generation=new AtomicInteger();
 int current(){return generation.get();}
 boolean isCurrent(int value){return generation.get()==value;}
 void cancel(){generation.incrementAndGet();}
 Runnable guard(Runnable work){final int expected=current();return ()->{if(isCurrent(expected))work.run();};}
}
