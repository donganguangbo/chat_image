package service

import (
	"context"
	"fmt"
	"io"
	"sync"
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

func (s *Server) taskCleanupLoop() {
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
					s.failTask(id, fmt.Errorf("worker panic: %v", r))
				}
			}()
			s.executeTask(id)
		}()
	}
}
