package worker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"bank_proto_microservice/internal/utils"
)

var (
	ErrQueueFull       = fmt.Errorf("очередь переполнена")
	ErrShutdownTimeout = fmt.Errorf("таймаут остановки воркеров")
)

type Job struct {
	ID      string
	Task    func() error
	RetryOn func(error) bool
	OnDone  func(error)
}

type PoolStats struct {
	TotalJobs     int64
	CompletedJobs int64
	FailedJobs    int64
	ActiveWorkers int
	QueuedJobs    int
}

type WorkerPool struct {
	workers    int
	jobQueue   chan Job
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	mu         sync.Mutex
	stats      PoolStats
	maxRetries int
}

func NewWorkerPool(workers int, queueSize int, maxRetries int) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())

	pool := &WorkerPool{
		workers:    workers,
		jobQueue:   make(chan Job, queueSize),
		ctx:        ctx,
		cancel:     cancel,
		maxRetries: maxRetries,
		stats: PoolStats{
			ActiveWorkers: workers,
		},
	}

	utils.LogSuccess("WorkerPool", "Создан пул воркеров (Воркеров: %d, Очередь: %d, Повторов: %d)", workers, queueSize, maxRetries)
	return pool
}

func (p *WorkerPool) Start() {
	utils.LogInfo("WorkerPool", "Запуск воркеров...")
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
	utils.LogSuccess("WorkerPool", "Все воркеры успешно запущены")
}

func (p *WorkerPool) worker(id int) {
	defer p.wg.Done()
	utils.LogDebug("WorkerPool", "Воркер #%d начал прослушивание очереди", id)

	for {
		select {
		case <-p.ctx.Done():
			utils.LogInfo("WorkerPool", "Воркер #%d завершает работу по сигналу контекста", id)
			return

		case job, ok := <-p.jobQueue:
			if !ok {
				utils.LogInfo("WorkerPool", "Воркер #%d: очередь закрыта, завершение", id)
				return
			}
			p.updateStats(0, -1)
			p.executeJob(id, job)
		}
	}
}

func (p *WorkerPool) executeJob(workerID int, job Job) {
	startTime := time.Now()
	var err error

	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		if attempt > 0 {
			utils.LogWarning("WorkerPool", "Воркер #%d: повторная попытка #%d для задачи %s", workerID, attempt, job.ID)
			time.Sleep(time.Millisecond * time.Duration(100*attempt))
		}

		err = job.Task()
		if err == nil {
			p.updateStats(1, 0)
			duration := time.Since(startTime)
			utils.LogSuccess("WorkerPool", "Воркер #%d: задача %s выполнена за %v", workerID, job.ID, duration)
			if job.OnDone != nil {
				job.OnDone(nil)
			}
			return
		}

		if job.RetryOn != nil && !job.RetryOn(err) {
			break
		}
	}

	p.updateStats(0, 0)
	p.mu.Lock()
	p.stats.FailedJobs++
	p.mu.Unlock()

	duration := time.Since(startTime)
	utils.LogError("WorkerPool", fmt.Sprintf("Воркер #%d: задача %s провалилась после %v", workerID, job.ID, duration), err)

	if job.OnDone != nil {
		job.OnDone(err)
	}
}

func (p *WorkerPool) Submit(job Job) error {
	select {
	case <-p.ctx.Done():
		return context.Canceled
	case p.jobQueue <- job:
		p.updateStats(0, 1)
		utils.LogDebug("WorkerPool", "Задача %s добавлена в очередь (в очереди: %d)", job.ID, p.GetStats().QueuedJobs)
		return nil
	default:
		utils.LogWarning("WorkerPool", "Очередь переполнена! Задача %s отклонена", job.ID)
		return ErrQueueFull
	}
}

func (p *WorkerPool) Shutdown(timeout time.Duration) error {
	utils.LogInfo("WorkerPool", "Остановка пула воркеров...")
	close(p.jobQueue)

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		utils.LogSuccess("WorkerPool", "Все воркеры успешно завершили работу")
		return nil
	case <-time.After(timeout):
		p.cancel()
		utils.LogWarning("WorkerPool", "Превышен таймаут остановки, принудительное завершение")
		return ErrShutdownTimeout
	}
}

func (p *WorkerPool) GetStats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	stats := p.stats
	stats.QueuedJobs = len(p.jobQueue)
	return stats
}

func (p *WorkerPool) updateStats(completed int64, queued int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stats.TotalJobs++
	p.stats.CompletedJobs += completed
	p.stats.QueuedJobs += queued
}

func GetCurrentTimeMs() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}
