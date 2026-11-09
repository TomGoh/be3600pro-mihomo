#!/usr/bin/env python3
# macOS offline-test shim: the shared open file description retains flock in sh.
import fcntl
import sys
import time
args=sys.argv[1:]
fd=int(args[-1]);flags=fcntl.LOCK_UN if '-u' in args else fcntl.LOCK_EX
if '-n' in args:flags|=fcntl.LOCK_NB
if '-w' in args:
    deadline=time.monotonic()+float(args[args.index('-w')+1])
    while True:
        try:fcntl.flock(fd,flags|fcntl.LOCK_NB);break
        except BlockingIOError:
            if time.monotonic()>=deadline:sys.exit(1)
            time.sleep(.01)
else:
    try:fcntl.flock(fd,flags)
    except BlockingIOError:sys.exit(1)
