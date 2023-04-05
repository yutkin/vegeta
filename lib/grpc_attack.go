package vegeta

import (
	"google.golang.org/grpc"
	"sync"
	"time"
	"context"
)

type GrpcAttacker struct {
	conn grpc.ClientConnInterface
	//dialer     *net.Dialer
	//client     http.Client
	stopch     chan struct{}
	workers    uint64
	maxWorkers uint64
	//maxBody    int64
	//redirects  int
	seqmu sync.Mutex
	seq   uint64
	began time.Time
	//chunked    bool
}

// Stop stops the current attack.
func (a *GrpcAttacker) Stop() {
	select {
	case <-a.stopch:
		return
	default:
		close(a.stopch)
	}
}

func (a *GrpcAttacker) Attack(tr GrpcTargeter, p Pacer, du time.Duration, name string) <-chan *Result {
	var wg sync.WaitGroup

	workers := a.workers
	if workers > a.maxWorkers {
		workers = a.maxWorkers
	}

	results := make(chan *Result)
	ticks := make(chan struct{})
	for i := uint64(0); i < workers; i++ {
		wg.Add(1)
		go a.attack(tr, name, &wg, ticks, results)
	}

	go func() {
		defer close(results)
		defer wg.Wait()
		defer close(ticks)

		began, count := time.Now(), uint64(0)
		for {
			elapsed := time.Since(began)
			if du > 0 && elapsed > du {
				return
			}

			wait, stop := p.Pace(elapsed, count)
			if stop {
				return
			}

			time.Sleep(wait)

			if workers < a.maxWorkers {
				select {
				case ticks <- struct{}{}:
					count++
					continue
				case <-a.stopch:
					return
				default:
					// all workers are blocked. start one more and try again
					workers++
					wg.Add(1)
					go a.attack(tr, name, &wg, ticks, results)
				}
			}

			select {
			case ticks <- struct{}{}:
				count++
			case <-a.stopch:
				return
			}
		}
	}()

	return results
}

func (a *GrpcAttacker) attack(tr GrpcTargeter, name string, workers *sync.WaitGroup, ticks <-chan struct{}, results chan<- *Result) {
	defer workers.Done()
	for range ticks {
		results <- a.hit(tr, name)
	}
}

func (a *GrpcAttacker) hit(tr GrpcTargeter, name string) *Result {
	var (
		res = Result{Attack: name}
		tgt GrpcTarget
		err error
	)

	a.seqmu.Lock()
	res.Timestamp = a.began.Add(time.Since(a.began))
	res.Seq = a.seq
	a.seq++
	a.seqmu.Unlock()

	defer func() {
		res.Latency = time.Since(res.Timestamp)
		if err != nil {
			res.Error = err.Error()
		}
	}()

	if err = tr(&tgt); err != nil {
		a.Stop()
		return &res
	}

	res.Method = tgt.Method

	err = a.conn.Invoke(context.TODO(), tgt.Method, tgt.Req, tgt.Resp, nil)
	if err != nil {
		res.Error = err.Error()
		return &res
	}

	return &res
}
