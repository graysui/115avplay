package ingestion

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type SchedulerConfig struct {
	Timezone string // e.g. "Asia/Shanghai"
	Hour     int    // default 4
	Minute   int    // default 0
}

type SyncScheduler struct {
	service  *IngestionService
	cfg      SchedulerConfig
	logger   *slog.Logger
	stopChan chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	running  bool
	lastSlot string
}

func NewSyncScheduler(service *IngestionService, cfg SchedulerConfig, logger *slog.Logger) *SyncScheduler {
	if cfg.Timezone == "" {
		cfg.Timezone = "Asia/Shanghai"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SyncScheduler{
		service:  service,
		cfg:      cfg,
		logger:   logger,
		stopChan: make(chan struct{}),
	}
}

func (s *SyncScheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}

	loc, err := time.LoadLocation(s.cfg.Timezone)
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}

	s.running = true
	s.wg.Add(1)

	go func() {
		defer s.wg.Done()
		s.logger.Info("starting ingestion daily scheduler", "timezone", s.cfg.Timezone, "hour", s.cfg.Hour, "minute", s.cfg.Minute)

		for {
			now := time.Now().In(loc)
			nextRun := time.Date(now.Year(), now.Month(), now.Day(), s.cfg.Hour, s.cfg.Minute, 0, 0, loc)
			if !nextRun.After(now) {
				nextRun = nextRun.Add(24 * time.Hour)
			}

			waitDuration := time.Until(nextRun)
			s.logger.Debug("next scheduled sync30d", "at", nextRun.Format(time.RFC3339), "wait", waitDuration)

			select {
			case <-s.stopChan:
				s.logger.Info("ingestion scheduler stopped")
				return
			case <-time.After(waitDuration):
				slot := nextRun.Format("2006-01-02")
				s.mu.Lock()
				if s.lastSlot == slot {
					s.mu.Unlock()
					continue
				}
				s.lastSlot = slot
				s.mu.Unlock()

				s.logger.Info("triggering daily 04:00 sync30d", "slot", slot)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
				_, err := s.service.Sync30D(ctx)
				if err != nil {
					s.logger.Warn("daily sync30d trigger error", "error", err)
				}
				cancel()
			}
		}
	}()

	return nil
}

func (s *SyncScheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	close(s.stopChan)
	s.mu.Unlock()

	s.wg.Wait()
}
