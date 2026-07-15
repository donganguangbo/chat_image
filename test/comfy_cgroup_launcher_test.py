import importlib.util
from collections import namedtuple
from pathlib import Path


SCRIPT = Path(__file__).parents[1] / "scripts" / "comfy_cgroup_launcher.py"
SPEC = importlib.util.spec_from_file_location("comfy_cgroup_launcher", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)

Memory = namedtuple("Memory", "total available percent used free")
GIB = 1024**3


def test_constrains_memory_to_cgroup():
    host = Memory(1024 * GIB, 900 * GIB, 12.0, 124 * GIB, 800 * GIB)

    memory = MODULE._constrain_virtual_memory(host, 120 * GIB, 26 * GIB)

    assert memory.total == 120 * GIB
    assert memory.available == 94 * GIB
    assert memory.used == 26 * GIB
    assert memory.free == 94 * GIB
