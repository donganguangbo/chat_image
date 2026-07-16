import importlib.util
from pathlib import Path


SCRIPT = Path(__file__).parents[1] / "scripts" / "health_monitor.py"
SPEC = importlib.util.spec_from_file_location("health_monitor", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class FakeSupervisor:
    def __init__(self, state="RUNNING", start=0):
        self.state = state
        self.start = start
        self.started = []
        self.stopped = []

    def getProcessInfo(self, name):
        return {"name": name, "statename": self.state, "start": self.start}

    def startProcess(self, name, wait):
        self.started.append((name, wait))

    def stopProcess(self, name, wait):
        self.stopped.append((name, wait))


class FakeRPC:
    def __init__(self, supervisor):
        self.supervisor = supervisor


def make_monitor(supervisor, checker, threshold=3, grace=0):
    return MODULE.HealthMonitor(
        FakeRPC(supervisor),
        [MODULE.Service("comfy_6006", "http://127.0.0.1:6006/system_stats", grace)],
        failure_threshold=threshold,
        request_timeout=1,
        retry_interval=30,
        checker=checker,
        logger=lambda *args, **kwargs: None,
    )


def test_restarts_after_consecutive_health_failures():
    supervisor = FakeSupervisor()
    monitor = make_monitor(supervisor, lambda *_: (False, "timeout"))

    monitor.tick(now=100)
    monitor.tick(now=110)
    monitor.tick(now=120)

    assert supervisor.stopped == [("comfy_6006", True)]
    assert supervisor.started == [("comfy_6006", False)]


def test_does_not_probe_during_startup_grace():
    supervisor = FakeSupervisor(start=100)
    checks = []
    monitor = make_monitor(
        supervisor,
        lambda *_: (checks.append(True) or False, "timeout"),
        threshold=1,
        grace=120,
    )

    monitor.tick(now=150)

    assert checks == []
    assert supervisor.stopped == []


def test_retries_fatal_service_with_backoff():
    supervisor = FakeSupervisor(state="FATAL")
    monitor = make_monitor(supervisor, lambda *_: (True, ""))

    monitor.tick(now=100)
    monitor.tick(now=110)
    monitor.tick(now=131)

    assert supervisor.started == [
        ("comfy_6006", False),
        ("comfy_6006", False),
    ]
