#!/usr/bin/env python3
import argparse
import datetime
import os
import signal
import threading
import time
import urllib.error
import urllib.request


class Service:
    def __init__(self, name, url, startup_grace):
        self.name = name
        self.url = url
        self.startup_grace = startup_grace


def http_check(url, timeout):
    request = urllib.request.Request(url, headers={"User-Agent": "chat-image-health-monitor"})
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            if 200 <= response.status < 400:
                return True, ""
            return False, f"HTTP {response.status}"
    except (OSError, TimeoutError, urllib.error.URLError) as exc:
        return False, str(exc)


class HealthMonitor:
    def __init__(
        self,
        rpc,
        services,
        failure_threshold,
        request_timeout,
        retry_interval,
        checker=http_check,
        logger=print,
    ):
        self.rpc = rpc
        self.services = services
        self.failure_threshold = failure_threshold
        self.request_timeout = request_timeout
        self.retry_interval = retry_interval
        self.checker = checker
        self.logger = logger
        self.failures = {service.name: 0 for service in services}
        self.last_action = {}

    def log(self, message):
        timestamp = datetime.datetime.now().astimezone().isoformat(timespec="seconds")
        self.logger(f"{timestamp} {message}", flush=True)

    def tick(self, now=None):
        now = time.time() if now is None else now
        for service in self.services:
            try:
                info = self.rpc.supervisor.getProcessInfo(service.name)
            except Exception as exc:
                self.log(f"[error] cannot inspect {service.name}: {exc}")
                continue

            state = info.get("statename", "UNKNOWN")
            if state == "FATAL":
                self._recover_fatal(service, now)
                continue
            if state != "RUNNING":
                self.failures[service.name] = 0
                continue
            if now - info.get("start", now) < service.startup_grace:
                self.failures[service.name] = 0
                continue

            healthy, reason = self.checker(service.url, self.request_timeout)
            if healthy:
                if self.failures[service.name]:
                    self.log(f"[ok] {service.name} health recovered")
                self.failures[service.name] = 0
                continue

            self.failures[service.name] += 1
            failures = self.failures[service.name]
            self.log(
                f"[warn] {service.name} health failed "
                f"({failures}/{self.failure_threshold}): {reason}"
            )
            if failures >= self.failure_threshold:
                self._restart(service, now, reason)

    def _recover_fatal(self, service, now):
        if now - self.last_action.get(service.name, 0) < self.retry_interval:
            return
        self.last_action[service.name] = now
        try:
            self.rpc.supervisor.startProcess(service.name, False)
            self.log(f"[restart] retrying FATAL service {service.name}")
        except Exception as exc:
            self.log(f"[error] cannot retry {service.name}: {exc}")

    def _restart(self, service, now, reason):
        self.failures[service.name] = 0
        self.last_action[service.name] = now
        try:
            self.rpc.supervisor.stopProcess(service.name, True)
        except Exception as exc:
            self.log(f"[warn] stop before restart failed for {service.name}: {exc}")
        try:
            self.rpc.supervisor.startProcess(service.name, False)
            self.log(f"[restart] restarted unhealthy {service.name}: {reason}")
        except Exception as exc:
            self.log(f"[error] cannot restart {service.name}: {exc}")


def parse_assignments(values, value_parser=str):
    parsed = {}
    for value in values:
        if "=" not in value:
            raise ValueError(f"expected NAME=VALUE, got {value!r}")
        name, raw = value.split("=", 1)
        if not name or not raw:
            raise ValueError(f"expected NAME=VALUE, got {value!r}")
        parsed[name] = value_parser(raw)
    return parsed


def parse_args():
    parser = argparse.ArgumentParser(description="Restart unhealthy Supervisor services")
    parser.add_argument("--target", action="append", required=True, metavar="NAME=URL")
    parser.add_argument("--grace", action="append", default=[], metavar="NAME=SECONDS")
    parser.add_argument("--interval", type=float, default=10)
    parser.add_argument("--failures", type=int, default=6)
    parser.add_argument("--timeout", type=float, default=5)
    parser.add_argument("--retry-interval", type=float, default=30)
    args = parser.parse_args()
    try:
        targets = parse_assignments(args.target)
        grace = parse_assignments(args.grace, float)
    except ValueError as exc:
        parser.error(str(exc))
    args.services = [Service(name, url, grace.get(name, 0)) for name, url in targets.items()]
    return args


def main():
    args = parse_args()
    from supervisor import childutils

    rpc = childutils.getRPCInterface(os.environ)
    monitor = HealthMonitor(
        rpc,
        args.services,
        args.failures,
        args.timeout,
        args.retry_interval,
    )
    stop = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stop.set())
    signal.signal(signal.SIGINT, lambda *_: stop.set())
    monitor.log(
        "[start] monitoring "
        + ", ".join(f"{service.name}={service.url}" for service in args.services)
    )
    while not stop.is_set():
        monitor.tick()
        stop.wait(args.interval)
    monitor.log("[stop] health monitor stopped")


if __name__ == "__main__":
    main()
