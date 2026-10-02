package metrics

import "time"

const (
	// ProbeFailEvery is the gap between attempts while a probe is failing.
	ProbeFailEvery = 5 * time.Second
	// ProbeOKEvery is the gap after a success, and after failover is armed,
	// so a dead node does not generate a one-second error loop.
	ProbeOKEvery = 120 * time.Second
	// ProbeFailLimit is consecutive failures before a failover recommendation.
	ProbeFailLimit = 3
)

// Probe schedules one active request. It does not dial.
type Probe struct {
	fails int
	next  time.Time
	armed bool
}

type ProbeResult struct {
	Fails    int
	Failover bool
}

// Due reports whether an attempt should run at now.
func (p *Probe) Due(now time.Time) bool {
	return p.next.IsZero() || !now.Before(p.next)
}

// Observe records one attempt. Failover becomes true on the third
// consecutive failure and stays recommended until a success.
func (p *Probe) Observe(now time.Time, ok bool) ProbeResult {
	if ok {
		p.fails = 0
		p.armed = false
		p.next = now.Add(ProbeOKEvery)
		return ProbeResult{}
	}
	if p.fails < ProbeFailLimit {
		p.fails++
	}
	p.armed = p.fails >= ProbeFailLimit
	if p.armed {
		p.next = now.Add(ProbeOKEvery)
	} else {
		p.next = now.Add(ProbeFailEvery)
	}
	return ProbeResult{Fails: p.fails, Failover: p.armed}
}
