package metrics

// RingLen is five minutes of one-second samples.
const RingLen = 300

// Ring is a fixed observation buffer, oldest sample first when read.
type Ring struct {
	buf []Sample
	i   int
	n   int
}

func (r *Ring) Add(s Sample) {
	if r.buf == nil {
		r.buf = make([]Sample, RingLen)
	}
	r.buf[r.i] = s
	r.i = (r.i + 1) % RingLen
	if r.n < RingLen {
		r.n++
	}
}

// Last returns up to n samples, oldest first.
func (r *Ring) Last(n int) []Sample {
	if n > r.n {
		n = r.n
	}
	if n <= 0 {
		return nil
	}
	out := make([]Sample, n)
	start := (r.i - n + RingLen) % RingLen
	for i := 0; i < n; i++ {
		out[i] = r.buf[(start+i)%RingLen]
	}
	return out
}

func (r *Ring) Len() int { return r.n }
