import importlib.util
from pathlib import Path


SCRIPT = Path(__file__).parents[1] / "scripts" / "memory_guard.py"
SPEC = importlib.util.spec_from_file_location("memory_guard", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)
GIB = MODULE.GIB


def test_configures_memory_high_without_raising_lower_limit(tmp_path):
    (tmp_path / "memory.max").write_text(str(120 * GIB), encoding="ascii")
    high = tmp_path / "memory.high"
    high.write_text("max", encoding="ascii")

    assert MODULE.configure_memory_high(tmp_path, 108 * GIB, logger=lambda *_: None)
    assert high.read_text(encoding="ascii") == str(108 * GIB)

    assert MODULE.configure_memory_high(tmp_path, 110 * GIB, logger=lambda *_: None)
    assert high.read_text(encoding="ascii") == str(108 * GIB)


def test_reclaims_only_when_all_comfy_queues_are_idle(tmp_path):
    current = tmp_path / "memory.current"
    current.write_text(str(106 * GIB), encoding="ascii")
    freed = []
    advised = []
    queue_states = {"http://comfy-1": True, "http://comfy-2": False}
    guard = MODULE.MemoryGuard(
        comfy_urls=list(queue_states),
        memory_current_path=current,
        reclaim_threshold=105 * GIB,
        cooldown=300,
        request_timeout=1,
        free_delay=0,
        models_root=tmp_path,
        excluded_roots=[],
        min_file_bytes=1,
        idle_checker=lambda url, _: queue_states[url],
        free_requester=lambda url, _: freed.append(url),
        adviser=lambda *args: advised.append(args) or (1, GIB, 0),
        sleeper=lambda _: None,
        logger=lambda *_: None,
    )

    assert not guard.tick(now=100)
    assert freed == []
    assert advised == []

    queue_states["http://comfy-2"] = True
    assert guard.tick(now=110)
    assert freed == ["http://comfy-1", "http://comfy-2"]
    assert len(advised) == 1

    assert not guard.tick(now=120)
    assert len(advised) == 1


def test_page_cache_advice_excludes_llama_and_small_files(tmp_path):
    models = tmp_path / "models"
    comfy = models / "checkpoints"
    llama = models / "llama"
    comfy.mkdir(parents=True)
    llama.mkdir()
    (comfy / "large.safetensors").write_bytes(b"x" * 16)
    (comfy / "small.json").write_bytes(b"x")
    (llama / "model.gguf").write_bytes(b"x" * 16)
    advised = []

    files, size, errors = MODULE.advise_model_cache(
        models,
        [llama],
        min_file_bytes=8,
        advise_func=lambda fd: advised.append(fd),
    )

    assert files == 1
    assert size == 16
    assert errors == 0
    assert len(advised) == 1
