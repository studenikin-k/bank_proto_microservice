package worker

import (
	"context"
	"errors"
	"expvar"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrQueueFull  = errors.New("очередь воркеров переполнена")
	ErrPoolClosed = errors.New("пул воркеров остановлен")
)

// Job — задача пула.
type Job struct {
	ID string
	// Task выполняет работу. Повторяется только если RetryOn(err) == true,
	// поэтому повторять можно лишь идемпотентные операции.
	Task    func(ctx context.Context) error
	RetryOn func(error) bool
	// OnDone вызывается один раз с итоговой ошибкой (nil — успех).
	OnDone func(error)
}

type Stats struct {
	Submitted int64 `json:"submitted"`
	Rejected  int64 `json:"rejected"`
	Completed int64 `json:"completed"`
	Failed    int64 `json:"failed"`
	Retries   int64 `json:"retries"`
	Queued    int   `json:"queued"`
	Workers   int   `json:"workers"`
}

type WorkerPool struct {
	workers    int
	maxRetries int
	jobs       chan Job

	mu     sync.RWMutex // защищает closed и отправку в jobs от закрытия канала
	closed bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	submitted, rejected, completed, failed, retries atomic.Int64
}

func NewWorkerPool(workers, queueSize, maxRetries int) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())
	p := &WorkerPool{
		workers:    workers,
		maxRetries: maxRetries,
		jobs:       make(chan Job, queueSize),
		ctx:        ctx,
		cancel:     cancel,
	}
	if expvar.Get("worker_pool") == nil {
		expvar.Publish("worker_pool", expvar.Func(func() any { return p.Stats() }))
	}
	return p
}

func (p *WorkerPool) Start() {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.loop()
	}
}

func (p *WorkerPool) loop() {
	defer p.wg.Done()
	for job := range p.jobs {
		p.execute(job)
	}
}

func (p *WorkerPool) execute(job Job) {
	var err error
	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		if attempt > 0 {
			p.retries.Add(1)
			select {
			case <-p.ctx.Done():
			case <-time.After(time.Duration(100*attempt) * time.Millisecond):
			}
		}
		if err = job.Task(p.ctx); err == nil || p.ctx.Err() != nil || job.RetryOn == nil || !job.RetryOn(err) {
			break
		}
		slog.Debug("задача будет повторена", "job", job.ID, "attempt", attempt+1, "err", err)
	}

	if err == nil {
		p.completed.Add(1)
	} else {
		p.failed.Add(1)
	}
	if job.OnDone != nil {
		job.OnDone(err)
	}
}

// Submit ставит задачу в очередь без ожидания: при переполнении сразу возвращает ErrQueueFull.
func (p *WorkerPool) Submit(job Job) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return ErrPoolClosed
	}
	select {
	case p.jobs <- job:
		p.submitted.Add(1)
		return nil
	default:
		p.rejected.Add(1)
		return ErrQueueFull
	}
}

// Shutdown перестаёт принимать задачи и ждёт, пока воркеры доделают очередь.
// По истечении timeout отменяет контекст выполняющихся задач.
func (p *WorkerPool) Shutdown(timeout time.Duration) error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.jobs)
	}
	p.mu.Unlock()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		p.cancel()
		return nil
	case <-time.After(timeout):
		p.cancel()
		<-done
		return errors.New("таймаут остановки пула: выполнение задач прервано")
	}
}

func (p *WorkerPool) Stats() Stats {
	return Stats{
		Submitted: p.submitted.Load(),
		Rejected:  p.rejected.Load(),
		Completed: p.completed.Load(),
		Failed:    p.failed.Load(),
		Retries:   p.retries.Load(),
		Queued:    len(p.jobs),
		Workers:   p.workers,
	}
}
