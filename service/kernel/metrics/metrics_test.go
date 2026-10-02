package metrics

import (
	"math"
	"testing"
	"time"
)

func TestSamplePerTagAndDemand(t *testing.T) {
	var c Counter
	now := time.Unix(100, 0)
	stats := []Stat{
		{Name: "outbound>>>proxy>>>traffic>>>uplink", Value: 1000},
		{Name: "outbound>>>proxy>>>traffic>>>downlink", Value: 2000},
		{Name: "outbound>>>cc50528>>>traffic>>>downlink", Value: 50},
		{Name: "outbound>>>direct>>>traffic>>>downlink", Value: 9_000},
		{Name: "inbound>>>socks_ipv4>>>traffic>>>downlink", Value: 2_000},
		{Name: "inbound>>>api-in_ipv4>>>traffic>>>downlink", Value: 9_000},
	}
	first := c.Sample(stats, now)
	if first.Up != 0 || first.Down != 0 || first.Demand != 0 || first.DownTotal != 2050 {
		t.Fatalf("first = %+v", first)
	}
	stats[0].Value += 100
	stats[1].Value += 2_000
	stats[2].Value += 50
	stats[4].Value += 1_500
	second := c.Sample(stats, now.Add(time.Second))
	if second.Outbounds["proxy"].Down != 2000 || second.Outbounds["proxy"].Up != 100 {
		t.Fatalf("proxy = %+v", second.Outbounds["proxy"])
	}
	if second.Outbounds["cc50528"].Down != 50 {
		t.Fatalf("cc50528 = %+v", second.Outbounds["cc50528"])
	}
	if _, ok := second.Outbounds["direct"]; ok {
		t.Fatal("direct was included")
	}
	if second.Down != 2050 || second.Demand != 1500 {
		t.Fatalf("sum down %v demand %v", second.Down, second.Demand)
	}
}

func TestSampleIgnoresCounterReset(t *testing.T) {
	var c Counter
	now := time.Unix(100, 0)
	stats := []Stat{{Name: "outbound>>>proxy>>>traffic>>>downlink", Value: 5000}}
	c.Sample(stats, now)
	stats[0].Value = 10
	got := c.Sample(stats, now.Add(time.Second))
	if got.Down != 0 || got.DownTotal != 10 {
		t.Fatalf("reset = %+v", got)
	}
}

func TestRingOrder(t *testing.T) {
	var r Ring
	for i := 0; i < RingLen+5; i++ {
		r.Add(Sample{Down: float64(i)})
	}
	last := r.Last(3)
	if len(last) != 3 || last[0].Down != float64(RingLen+2) || last[2].Down != float64(RingLen+4) {
		t.Fatalf("last = %+v", last)
	}
}

func TestProbeBackoffAndFailover(t *testing.T) {
	var p Probe
	now := time.Unix(1_000, 0)
	if !p.Due(now) {
		t.Fatal("first probe should be due")
	}
	if got := p.Observe(now, false); got.Failover || got.Fails != 1 {
		t.Fatalf("first fail = %+v", got)
	}
	if p.Due(now.Add(4 * time.Second)) {
		t.Fatal("probe ran inside the 5s failure gap")
	}
	now = now.Add(ProbeFailEvery)
	if got := p.Observe(now, false); got.Failover {
		t.Fatal("second failure armed failover")
	}
	now = now.Add(ProbeFailEvery)
	if got := p.Observe(now, false); !got.Failover || got.Fails != 3 {
		t.Fatalf("third fail = %+v", got)
	}
	if p.Due(now.Add(30 * time.Second)) {
		t.Fatal("armed probe did not back off to 120s")
	}
	now = now.Add(ProbeOKEvery)
	if got := p.Observe(now, true); got.Failover || got.Fails != 0 {
		t.Fatalf("recovery = %+v", got)
	}
	if p.Due(now.Add(30 * time.Second)) {
		t.Fatal("success did not back off to 120s")
	}
}

func TestEvaluateHoldsAtMeasuredCeiling(t *testing.T) {
	// 2026-10-02 Brutal 160/40: 15.25 MB/s against a 20 MB/s cap.
	in := Input{
		CapDown: 20_000_000, CapUp: 5_000_000,
		Down10: 15.25e6, Down30: 15.25e6, Demand10: 15.25e6,
		Samples: 30, SinceChange: 4 * time.Minute,
	}
	got := Evaluate(in)
	if got.Action != "hold" || got.Reason != "in_band" {
		t.Fatalf("decision = %+v", got)
	}
	if math.Abs(got.Utilize-0.7625) > 0.001 {
		t.Fatalf("utilization = %v", got.Utilize)
	}
}

func TestEvaluateRaisesWhenFull(t *testing.T) {
	in := Input{
		CapDown: 20_000_000, CapUp: 5_000_000,
		Down10: 19.2e6, Down30: 19.2e6, Demand10: 19.2e6,
		Samples: 30, SinceChange: 4 * time.Minute,
	}
	got := Evaluate(in)
	if got.Action != "raise" || got.NextDownBps != 22_000_000 {
		t.Fatalf("decision = %+v", got)
	}
}

func TestEvaluateRevertsThe200MbpsDrop(t *testing.T) {
	// Cap was raised to 25 MB/s (up_mbps 200). Goodput fell from 15.25 to 12.10.
	in := Input{
		CapDown: 25_000_000, CapUp: 6_250_000,
		PrevCapDown: 20_000_000, PrevCapUp: 5_000_000,
		Down10: 12.10e6, Down30: 12.10e6, Demand10: 12.10e6,
		Samples: 30, SinceChange: 10 * time.Second,
		LastAction: "raise", BaselineDown: 15.25e6,
	}
	got := Evaluate(in)
	if got.Action != "revert" || got.Reason != "goodput_drop" || got.NextDownBps != 20_000_000 {
		t.Fatalf("decision = %+v", got)
	}
}

func TestEvaluateIdleAndWarming(t *testing.T) {
	warm := Evaluate(Input{CapDown: 20_000_000, CapUp: 5_000_000, Samples: 5, Down10: 19e6})
	if warm.Reason != "warming" {
		t.Fatalf("warming = %+v", warm)
	}
	idle := Evaluate(Input{
		CapDown: 20_000_000, CapUp: 5_000_000,
		Down10: 1000, Down30: 1000, Samples: 30,
	})
	if idle.Reason != "idle" {
		t.Fatalf("idle = %+v", idle)
	}
}
