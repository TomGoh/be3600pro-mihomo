"""Atomic local heartbeat publishing; callers must copy atomically on remote hosts too."""
import os
from pathlib import Path
import tempfile


def heartbeat(path,uptime_seconds):
    if isinstance(uptime_seconds,bool) or not isinstance(uptime_seconds,int) or not 0<=uptime_seconds<=999999999:
        raise ValueError('heartbeat must be a canonical nonnegative uptime second')
    path=Path(path);temporary=None
    try:
        fd,temporary=tempfile.mkstemp(prefix='.lease-',dir=path.parent)
        with os.fdopen(fd,'w') as target:
            target.write(str(uptime_seconds)+'\n');target.flush();os.fsync(target.fileno())
        os.replace(temporary,path);temporary=None
    finally:
        if temporary is not None:os.unlink(temporary)
