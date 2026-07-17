#!/usr/bin/env python3
import argparse
import datetime
import json
import os
import signal
import stat
import threading
import time
import urllib.request


GIB = 1024**3
MIB = 1024**2


def log(message):
    timestamp = datetime.datetime.now().astimezone().isoformat(timespec="seconds")
    print(f"{timestamp} {message}", flush=True)


def read_memory_value(path):
    try:
        value = open(path, encoding="ascii").read().strip()
        return None if value == "max" else int(value)
    except (OSError, ValueError):
        return None


def read_memory_stat(path, key):
    try:
        with open(path, encoding="ascii") as file:
            for line in file:
                name, value = line.split()
                if name == key:
                    return int(value)
    except (OSError, ValueError):
        return None
    return None


def configure_memory_high(cgroup_dir, target_bytes, logger=log):
    high_path = os.path.join(cgroup_dir, "memory.high")
    max_path = os.path.join(cgroup_dir, "memory.max")
    if not os.path.exists(high_path):
        logger(f"[warn] cgroup memory.high is unavailable: {high_path}")
        return False

    hard_limit = read_memory_value(max_path)
    if hard_limit is not None and target_bytes >= hard_limit:
        logger(
            f"[warn] memory.high target {target_bytes / GIB:.1f} GiB must be below "
            f"memory.max {hard_limit / GIB:.1f} GiB"
        )
        return False

    current_high = read_memory_value(high_path)
    if current_high is not None and current_high <= target_bytes:
        logger(f"[memory.high] keeping lower existing limit {current_high / GIB:.1f} GiB")
        return True

    try:
        with open(high_path, "w", encoding="ascii") as file:
            file.write(str(target_bytes))
    except OSError as exc:
        logger(f"[warn] cannot set {high_path}: {exc}")
        return False

    logger(f"[memory.high] set cgroup soft limit to {target_bytes / GIB:.1f} GiB")
    return True


def request_json(url, method="GET", payload=None, timeout=5):
    body = None if payload is None else json.dumps(payload).encode("utf-8")
    request = urllib.request.Request(
        url,
        data=body,
        method=method,
        headers={
            "Content-Type": "application/json",
            "User-Agent": "chat-image-memory-guard",
        },
    )
    with urllib.request.urlopen(request, timeout=timeout) as response:
        raw = response.read()
    return json.loads(raw) if raw else None


def comfy_is_idle(base_url, timeout):
    queue = request_json(f"{base_url.rstrip('/')}/queue", timeout=timeout)
    if not isinstance(queue, dict):
        raise ValueError("ComfyUI /queue did not return an object")
    running = queue.get("queue_running")
    pending = queue.get("queue_pending")
    if not isinstance(running, list) or not isinstance(pending, list):
        raise ValueError("ComfyUI /queue response is missing queue lists")
    return not running and not pending


def free_comfy_memory(base_url, timeout):
    request_json(
        f"{base_url.rstrip('/')}/free",
        method="POST",
        payload={"unload_models": True, "free_memory": True},
        timeout=timeout,
    )


def _is_below(path, parent):
    try:
        return os.path.commonpath((os.path.abspath(path), parent)) == parent
    except ValueError:
        return False


def advise_model_cache(models_root, excluded_roots, min_file_bytes, advise_func=None):
    models_root = os.path.abspath(models_root)
    excluded_roots = tuple(os.path.abspath(path) for path in excluded_roots)
    if advise_func is None:
        if not hasattr(os, "posix_fadvise") or not hasattr(os, "POSIX_FADV_DONTNEED"):
            raise RuntimeError("POSIX_FADV_DONTNEED is unavailable on this system")
        advise_func = lambda fd: os.posix_fadvise(fd, 0, 0, os.POSIX_FADV_DONTNEED)

    advised_files = 0
    advised_bytes = 0
    errors = 0
    open_flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0)
    for root, directories, filenames in os.walk(models_root, followlinks=False):
        directories[:] = [
            name
            for name in directories
            if not any(_is_below(os.path.join(root, name), path) for path in excluded_roots)
        ]
        for filename in filenames:
            path = os.path.join(root, filename)
            if any(_is_below(path, excluded) for excluded in excluded_roots):
                continue
            try:
                info = os.stat(path)
                if not stat.S_ISREG(info.st_mode) or info.st_size < min_file_bytes:
                    continue
                fd = os.open(path, open_flags)
                try:
                    advise_func(fd)
                finally:
                    os.close(fd)
                advised_files += 1
                advised_bytes += info.st_size
            except OSError:
                errors += 1
    return advised_files, advised_bytes, errors


class MemoryGuard:
    def __init__(
        self,
        comfy_urls,
        memory_current_path,
        reclaim_threshold,
        cooldown,
        request_timeout,
        free_delay,
        models_root,
        excluded_roots,
        min_file_bytes,
        logger=log,
        idle_checker=comfy_is_idle,
        free_requester=free_comfy_memory,
        adviser=advise_model_cache,
        sleeper=time.sleep,
    ):
        self.comfy_urls = comfy_urls
        self.memory_current_path = memory_current_path
        self.reclaim_threshold = reclaim_threshold
        self.cooldown = cooldown
        self.request_timeout = request_timeout
        self.free_delay = free_delay
        self.models_root = models_root
        self.excluded_roots = excluded_roots
        self.min_file_bytes = min_file_bytes
        self.logger = logger
        self.idle_checker = idle_checker
        self.free_requester = free_requester
        self.adviser = adviser
        self.sleeper = sleeper
        self.last_reclaim = float("-inf")

    def tick(self, now=None):
        now = time.time() if now is None else now
        current = read_memory_value(self.memory_current_path)
        if current is None or current < self.reclaim_threshold:
            return False
        if now - self.last_reclaim < self.cooldown:
            return False

        memory_stat_path = os.path.join(os.path.dirname(self.memory_current_path), "memory.stat")
        file_cache = read_memory_stat(memory_stat_path, "file")
        try:
            if not all(self.idle_checker(url, self.request_timeout) for url in self.comfy_urls):
                return False
            for url in self.comfy_urls:
                self.free_requester(url, self.request_timeout)
        except Exception as exc:
            self.logger(f"[warn] cannot prepare idle ComfyUI memory reclaim: {exc}")
            return False

        self.last_reclaim = now
        self.sleeper(self.free_delay)
        try:
            if not all(self.idle_checker(url, self.request_timeout) for url in self.comfy_urls):
                self.logger("[skip] ComfyUI received a task while memory was being released")
                return False
            files, advised_bytes, errors = self.adviser(
                self.models_root,
                self.excluded_roots,
                self.min_file_bytes,
            )
        except Exception as exc:
            self.logger(f"[warn] model page-cache reclaim failed: {exc}")
            return False

        after = read_memory_value(self.memory_current_path)
        file_cache_after = read_memory_stat(memory_stat_path, "file")
        after_text = "unknown" if after is None else f"{after / GIB:.1f} GiB"
        cache_text = "unknown" if file_cache is None else f"{file_cache / GIB:.1f} GiB"
        cache_after_text = (
            "unknown" if file_cache_after is None else f"{file_cache_after / GIB:.1f} GiB"
        )
        self.logger(
            f"[reclaim] cgroup={current / GIB:.1f} GiB -> {after_text}; "
            f"file_cache={cache_text} -> {cache_after_text}; "
            f"advised {files} files/{advised_bytes / GIB:.1f} GiB; errors={errors}"
        )
        return True


def parse_args():
    parser = argparse.ArgumentParser(description="Protect ComfyUI from cgroup memory pressure")
    parser.add_argument("--comfy-url", action="append", required=True)
    parser.add_argument("--cgroup-dir", default="/sys/fs/cgroup")
    parser.add_argument("--memory-high-gib", type=float, default=108)
    parser.add_argument("--reclaim-threshold-gib", type=float, default=105)
    parser.add_argument("--interval", type=float, default=10)
    parser.add_argument("--cooldown", type=float, default=300)
    parser.add_argument("--startup-grace", type=float, default=120)
    parser.add_argument("--request-timeout", type=float, default=5)
    parser.add_argument("--free-delay", type=float, default=2)
    parser.add_argument("--models-root", required=True)
    parser.add_argument("--exclude-root", action="append", default=[])
    parser.add_argument("--min-file-size-mib", type=float, default=64)
    args = parser.parse_args()
    for name in (
        "memory_high_gib",
        "reclaim_threshold_gib",
        "interval",
        "cooldown",
        "request_timeout",
        "min_file_size_mib",
    ):
        if getattr(args, name) <= 0:
            parser.error(f"--{name.replace('_', '-')} must be greater than zero")
    for name in ("startup_grace", "free_delay"):
        if getattr(args, name) < 0:
            parser.error(f"--{name.replace('_', '-')} must not be negative")
    return args


def main():
    args = parse_args()
    configure_memory_high(args.cgroup_dir, int(args.memory_high_gib * GIB))
    guard = MemoryGuard(
        comfy_urls=args.comfy_url,
        memory_current_path=os.path.join(args.cgroup_dir, "memory.current"),
        reclaim_threshold=int(args.reclaim_threshold_gib * GIB),
        cooldown=args.cooldown,
        request_timeout=args.request_timeout,
        free_delay=args.free_delay,
        models_root=args.models_root,
        excluded_roots=args.exclude_root,
        min_file_bytes=int(args.min_file_size_mib * MIB),
    )

    stop = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stop.set())
    signal.signal(signal.SIGINT, lambda *_: stop.set())
    log(
        f"[start] threshold={args.reclaim_threshold_gib:.1f} GiB "
        f"cooldown={args.cooldown:.0f}s ComfyUI={','.join(args.comfy_url)}"
    )
    stop.wait(args.startup_grace)
    while not stop.is_set():
        guard.tick()
        stop.wait(args.interval)
    log("[stop] memory guard stopped")


if __name__ == "__main__":
    main()
