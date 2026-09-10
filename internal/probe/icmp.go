package probe

import (
	"context"
	"net/netip"
	"sync"
	"time"
)

type PingOptions struct {
	Count       int
	Spacing     time.Duration
	Timeout     time.Duration
	PayloadSize int
}

func (o PingOptions) withDefaults() PingOptions {
	if o.Count <= 0 {
		o.Count = 10
	}
	if o.Spacing <= 0 {
		o.Spacing = 100 * time.Millisecond
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Second
	}
	if o.PayloadSize < 16 {
		o.PayloadSize = 32
	}
	return o
}

type PingResult struct {
	Sent      int             `json:"sent"`
	Received  int             `json:"received"`
	LossRatio float64         `json:"loss_ratio"`
	Stats     Stats           `json:"stats"`
	RTTs      []time.Duration `json:"-"`
}

// Ping sends a burst of ICMP echo requests and summarises the replies. A result
// with zero replies is returned without error: unanswered ICMP means "no ICMP
// answer", not necessarily "host down", and the caller decides what that means.
func Ping(ctx context.Context, ip netip.Addr, opts PingOptions) (*PingResult, error) {
	opts = opts.withDefaults()

	conn, err := openICMP(ip.Is6())
	if err != nil {
		return nil, err
	}
	defer conn.close()

	id := newProbeID()
	payload := make([]byte, opts.PayloadSize)
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}

	var mu sync.Mutex
	sendTimes := make(map[int]time.Time, opts.Count)
	rtts := make([]time.Duration, opts.Count)
	for i := range rtts {
		rtts[i] = -1
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 1500)
		for {
			reply, ok, err := conn.read(buf)
			if err != nil {
				return
			}
			now := time.Now()
			if !ok || reply.kind != replyEcho || reply.from != ip {
				continue
			}
			// A datagram socket has the kernel rewrite our id, so only raw
			// sockets can use it to tell concurrent probes apart.
			if !conn.datagram && reply.id != id {
				continue
			}
			mu.Lock()
			sent, known := sendTimes[reply.seq]
			if known && reply.seq < len(rtts) && rtts[reply.seq] < 0 {
				rtts[reply.seq] = now.Sub(sent)
			}
			mu.Unlock()
		}
	}()

	deadline := time.Now()
	for seq := 0; seq < opts.Count; seq++ {
		if ctx.Err() != nil {
			break
		}
		mu.Lock()
		sendTimes[seq] = time.Now()
		mu.Unlock()
		if err := conn.send(ip, id, seq, payload); err != nil {
			mu.Lock()
			delete(sendTimes, seq)
			mu.Unlock()
			continue
		}
		deadline = time.Now().Add(opts.Timeout)
		if seq < opts.Count-1 {
			if !sleepCtx(ctx, opts.Spacing) {
				break
			}
		}
	}

	waitForReplies(ctx, deadline)
	conn.close()
	wg.Wait()

	result := &PingResult{}
	mu.Lock()
	result.Sent = len(sendTimes)
	var ordered []time.Duration
	for _, rtt := range rtts {
		if rtt >= 0 {
			ordered = append(ordered, rtt)
		}
	}
	mu.Unlock()

	result.Received = len(ordered)
	result.RTTs = ordered
	result.Stats = Summarize(ordered)
	if result.Sent > 0 {
		result.LossRatio = float64(result.Sent-result.Received) / float64(result.Sent)
	}
	return result, nil
}

func waitForReplies(ctx context.Context, deadline time.Time) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return
	}
	sleepCtx(ctx, remaining)
}

// sleepCtx reports false if the context ended before the delay elapsed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
