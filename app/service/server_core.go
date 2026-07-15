package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

type objectStorage interface {
	UploadPrivate(context.Context, string, io.Reader, string) error
	PresignGetObject(context.Context, string, time.Duration) (string, error)
}

type Server struct {
	mu        sync.RWMutex
	workflows map[string]WorkflowConfig
	tasks     map[string]*Task
	queue     chan string

	comfyPool   *ComfyPool
	service     ServiceConfig
	workerCount int
	outputDir   string
	storage     objectStorage

	// readiness probe cache (see BackendHealth)
	healthMu      sync.Mutex
	healthAt      time.Time
	healthHealthy int
	healthTotal   int
	healthProbing bool
}

func NewServer(workflows map[string]WorkflowConfig, comfyPool *ComfyPool, service ServiceConfig, workerCount int, outputDir string, storage objectStorage) *Server {
	if workerCount <= 0 {
		workerCount = 1
	}
	s := &Server{
		workflows:   workflows,
		tasks:       map[string]*Task{},
		queue:       make(chan string, 256),
		comfyPool:   comfyPool,
		service:     service,
		workerCount: workerCount,
		outputDir:   outputDir,
		storage:     storage,
	}
	if comfyPool != nil {
		for i := 0; i < workerCount; i++ {
			go s.workerLoop(i + 1)
		}
		go s.taskCleanupLoop()
	}
	return s
}

// Shutdown fails every non-terminal task and best-effort interrupts the orphan
// Comfy jobs of running tasks. Call it AFTER the HTTP server has stopped
// accepting new requests (httpSrv.Shutdown) so no new task can be created mid-
// drain. This is the graceful-restart path: without it, a deploy restart leaves
// clients polling forever (tasks vanish from memory → 404) and GPU jobs running
// unowned.
//
// ponytail: covers SIGTERM/SIGINT (normal deploy restart). A hard SIGKILL/OOM
// still orphans in-flight tasks — add task persistence (Redis/file) if that
// becomes a real failure mode.
func (s *Server) Shutdown(ctx context.Context) {
	s.mu.Lock()
	orphanBackends := map[string]struct{}{}
	failed := 0
	now := time.Now().UTC()
	for _, t := range s.tasks {
		switch t.Status {
		case "queued", "running":
			if t.Status == "running" && t.Backend != "" {
				orphanBackends[t.Backend] = struct{}{}
			}
			t.Status = "failed"
			t.Progress = 100
			t.Message = "server restarting"
			t.Error = "gateway restarted before task completed"
			t.UpdatedAt = now
			failed++
		}
	}
	s.mu.Unlock()

	// Best-effort interrupt of orphan GPU jobs on the backends that had a running
	// task. ComfyUI /interrupt cancels the currently-executing prompt (no id), so
	// one call per distinct backend is the most we can do. Fan out CONCURRENTLY
	// under the shared ctx so a slow/black-holed backend can't consume the whole
	// deadline and starve the interrupts to its healthy siblings.
	if s.comfyPool != nil {
		var wg sync.WaitGroup
		for backend := range orphanBackends {
			c := s.comfyPool.ClientForURL(backend)
			if c == nil {
				continue
			}
			wg.Add(1)
			go func(backend string, c *ComfyClient) {
				defer wg.Done()
				if err := c.Interrupt(ctx); err != nil {
					log.Printf("[shutdown] interrupt backend=%s failed: %v", backend, err)
				}
			}(backend, c)
		}
		wg.Wait()
	}
	log.Printf("[shutdown] failed %d in-flight task(s), interrupted %d backend(s)", failed, len(orphanBackends))
}

// BackendHealth probes every Comfy backend's /system_stats and reports how many
// answered. Used by /readyz so orchestration/monitoring can tell "process is up"
// (liveness) from "can actually serve" (at least one backend reachable).
//
// Results are cached for a short TTL so that (a) frequent probes (k8s readiness
// polls every few seconds) and (b) unauthenticated /readyz floods don't re-fan
// a probe to every backend on every hit. Staleness up to the TTL is harmless for
// readiness.
//
// The probe runs on its OWN context, deliberately NOT the caller's request
// context: a client (or k8s probe) disconnecting mid-request must not cancel the
// shared probe and let it cache a false "0 healthy", which would poison /readyz
// for the whole TTL and pull a healthy pod out of rotation.
func (s *Server) BackendHealth() (healthy int, total int) {
	const healthTTL = 2 * time.Second
	const probeTimeout = 3 * time.Second
	s.healthMu.Lock()
	fresh := !s.healthAt.IsZero() && time.Since(s.healthAt) < healthTTL
	// Single-flight: if the cache is fresh, or another goroutine is already
	// probing, return the last known value rather than launching a duplicate
	// probe. This avoids a thundering herd AND the last-writer-wins race where a
	// slow stale probe (0 healthy) could clobber a fresher probe's good result.
	if fresh || s.healthProbing {
		h, t := s.healthHealthy, s.healthTotal
		s.healthMu.Unlock()
		return h, t
	}
	s.healthProbing = true
	s.healthMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	h, t := s.probeBackends(ctx)

	s.healthMu.Lock()
	s.healthHealthy, s.healthTotal, s.healthAt = h, t, time.Now()
	s.healthProbing = false
	s.healthMu.Unlock()
	return h, t
}

// probeBackends pings every backend CONCURRENTLY under the shared ctx, so one
// slow/black-holed backend can't consume the whole readiness deadline and make
// healthy peers report as down (which would falsely fail /readyz).
func (s *Server) probeBackends(ctx context.Context) (healthy int, total int) {
	if s.comfyPool == nil {
		return 0, 0
	}
	clients := s.comfyPool.Clients()
	var n int64
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *ComfyClient) {
			defer wg.Done()
			if err := c.SystemStats(ctx); err == nil {
				atomic.AddInt64(&n, 1)
			}
		}(c)
	}
	wg.Wait()
	return int(n), len(clients)
}

func (s *Server) taskCleanupLoop() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[cleanup panic] %v\n%s", r, debug.Stack())
			go s.taskCleanupLoop() // restart so terminal tasks keep getting GC'd
		}
	}()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.cleanupFinishedTasks()
	}
}

func (s *Server) cleanupFinishedTasks() {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, t := range s.tasks {
		switch t.Status {
		case "completed", "failed", "cancelled":
			if now.Sub(t.UpdatedAt) > taskRetention {
				delete(s.tasks, id)
			}
		}
	}
}

func (s *Server) workerLoop(_ int) {
	for id := range s.queue {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[worker panic] task=%s: %v\n%s", id, r, debug.Stack())
					s.failTask(id, fmt.Errorf("worker panic: %v", r))
				}
			}()
			s.executeTask(id)
		}()
	}
}
