// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"sync"
	"time"

	"github.com/zensonic/oci-reg-benchmark/internal/metrics"
	"github.com/zensonic/oci-reg-benchmark/internal/scenario"
)

// workerPool runs a changing number of workers for one scenario. Workers
// that are removed on a scale-down finish their current operation first, so
// scaling never produces artificial errors.
type workerPool struct {
	ctx       context.Context
	scenario  scenario.Scenario
	recorder  *metrics.Recorder
	thinkTime time.Duration
	timeout   time.Duration

	mu      sync.Mutex
	workers []chan struct{} // one stop channel per running worker
	nextID  int
	wg      sync.WaitGroup
}

func newWorkerPool(ctx context.Context, sc scenario.Scenario, recorder *metrics.Recorder, thinkTime, timeout time.Duration) *workerPool {
	return &workerPool{ctx: ctx, scenario: sc, recorder: recorder, thinkTime: thinkTime, timeout: timeout}
}

// Size is the number of running workers.
func (p *workerPool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.workers)
}

// Resize starts or stops workers until n are running.
func (p *workerPool) Resize(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for len(p.workers) < n {
		stop := make(chan struct{})
		p.workers = append(p.workers, stop)
		id := p.nextID // never reused, so tags stay unique
		p.nextID++
		p.wg.Add(1)
		go p.runWorker(id, stop)
	}
	for len(p.workers) > n {
		last := len(p.workers) - 1
		close(p.workers[last])
		p.workers = p.workers[:last]
	}
}

// Wait blocks until all stopped workers have finished.
func (p *workerPool) Wait() {
	p.wg.Wait()
}

func (p *workerPool) runWorker(id int, stop chan struct{}) {
	defer p.wg.Done()
	w := p.scenario.NewWorker(id)
	defer w.Close()

	for {
		select {
		case <-stop:
			return
		case <-p.ctx.Done():
			return
		default:
		}

		opCtx, cancel := context.WithTimeout(p.ctx, p.timeout) // LOAD-7
		res := w.RunOnce(opCtx)
		cancel()

		// An operation cut short by an abort is not a registry error.
		if res.Err != nil && p.ctx.Err() != nil {
			return
		}
		p.recorder.Record(res)

		if p.thinkTime > 0 { // LOAD-6
			select {
			case <-stop:
				return
			case <-p.ctx.Done():
				return
			case <-time.After(p.thinkTime):
			}
		}
	}
}
