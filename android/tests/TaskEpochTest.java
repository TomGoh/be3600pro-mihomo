package dev.jose.routerproxy;
import java.util.ArrayDeque;
import java.util.concurrent.atomic.AtomicInteger;
public final class TaskEpochTest {
 public static void main(String[] args){
  TaskEpoch epoch=new TaskEpoch();ArrayDeque<Runnable> queue=new ArrayDeque<>();AtomicInteger sent=new AtomicInteger();
  epoch.cancel(); // First emergency leaves an older network call in flight.
  queue.add(epoch.guard(()->sent.incrementAndGet())); // Enable queued behind it.
  epoch.cancel(); // Second emergency invalidates the queued Enable too.
  queue.remove().run();if(sent.get()!=0)throw new AssertionError("queued old Enable sent after second stop");
  queue.add(epoch.guard(()->sent.incrementAndGet()));queue.remove().run();if(sent.get()!=1)throw new AssertionError("new explicit request rejected");
  System.out.println("PASS: executor cancellation fence (stale queue + new intent)");
 }
}
