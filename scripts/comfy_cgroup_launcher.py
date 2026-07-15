#!/usr/bin/env python3
import os
import runpy
import sys


CGROUP_MEMORY_FILES = (
    ("/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory.current"),
    (
        "/sys/fs/cgroup/memory/memory.limit_in_bytes",
        "/sys/fs/cgroup/memory/memory.usage_in_bytes",
    ),
)


def _read_int(path):
    try:
        value = open(path, encoding="ascii").read().strip()
        return None if value == "max" else int(value)
    except (OSError, ValueError):
        return None


def _cgroup_usage():
    for limit_path, current_path in CGROUP_MEMORY_FILES:
        limit = _read_int(limit_path)
        current = _read_int(current_path)
        if limit is not None and current is not None:
            return limit, current
    return None


def _constrain_virtual_memory(host, limit, current):
    total = min(host.total, limit)
    if total == host.total:
        return host

    available = min(host.available, max(0, total - current))
    used = max(0, total - available)
    percent = 0.0 if total == 0 else used * 100.0 / total
    return host._replace(
        total=total,
        available=available,
        used=used,
        free=min(host.free, available),
        percent=percent,
    )


def main():
    import psutil

    host_virtual_memory = psutil.virtual_memory

    def cgroup_virtual_memory():
        host = host_virtual_memory()
        usage = _cgroup_usage()
        if usage is None:
            return host
        return _constrain_virtual_memory(host, *usage)

    psutil.virtual_memory = cgroup_virtual_memory
    memory = cgroup_virtual_memory()
    print(
        "[comfy-cgroup] total={:.1f} GiB available={:.1f} GiB".format(
            memory.total / 1024**3,
            memory.available / 1024**3,
        ),
        file=sys.stderr,
        flush=True,
    )

    main_path = os.path.join(os.getcwd(), "main.py")
    sys.path.insert(0, os.getcwd())
    sys.argv[0] = main_path
    runpy.run_path(main_path, run_name="__main__")


if __name__ == "__main__":
    main()
