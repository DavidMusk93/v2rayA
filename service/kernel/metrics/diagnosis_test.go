package metrics

import (
	"testing"
	"time"
)

func TestDiagnose(t *testing.T) {
	latest := map[string]float64{"proxy": 19.4e6, "cc50528": 0, "proxy8444": 0}
	at := Diagnose(DiagInput{CapDown: 20e6, PeakDown: 19.4e6, DemandPeak: 19.4e6, LatestDown: latest, Samples: 10})
	if at.Code != "at_cap" || at.CapRatio < AtCapRatio {
		t.Fatalf("at_cap = %+v", at)
	}

	under := Diagnose(DiagInput{CapDown: 20e6, PeakDown: 11e6, DemandPeak: 11e6, LatestDown: map[string]float64{"proxy": 11e6}, Samples: 10})
	if under.Code != "under_cap" {
		t.Fatalf("under_cap = %+v", under)
	}

	idle := Diagnose(DiagInput{CapDown: 20e6, PeakDown: 0, DemandPeak: 0, Samples: 10})
	if idle.Code != "idle" {
		t.Fatalf("idle = %+v", idle)
	}

	// A 30s average would call this idle. The short peak must not.
	burst := Diagnose(DiagInput{CapDown: 20e6, PeakDown: 19e6, DemandPeak: 1e6, LatestDown: map[string]float64{"proxy": 0}, Samples: 30})
	if burst.Code != "at_cap" {
		t.Fatalf("burst = %+v", burst)
	}

	dead := Diagnose(DiagInput{CapDown: 20e6, Samples: 1, ProbeDead: true})
	if dead.Code != "dead" {
		t.Fatalf("dead = %+v", dead)
	}

	miss := Diagnose(DiagInput{
		CapDown: 20e6, PeakDown: 2e6, DemandPeak: 2e6, Samples: 10,
		LatestDown: map[string]float64{"proxy": 0, "cc50528": 2e6},
	})
	if miss.Code != "misroute" || miss.ActiveTag != "cc50528" {
		t.Fatalf("misroute = %+v", miss)
	}

	over := Diagnose(DiagInput{
		CapDown: 20e6, PeakDown: 2.3e6, DemandPeak: 2.3e6, Samples: 10,
		LatestDown: map[string]float64{"proxy": 2.3e6},
		HaveIdle:   true, HaveBusy: true,
		IdleTTFB: 500 * time.Millisecond, BusyTTFB: 2400 * time.Millisecond,
	})
	if over.Code != "overshoot" {
		t.Fatalf("overshoot = %+v", over)
	}

	// On-cap with a calm probe is not overshoot.
	calm := Diagnose(DiagInput{
		CapDown: 20e6, PeakDown: 19.5e6, DemandPeak: 19.5e6, Samples: 10,
		LatestDown: map[string]float64{"proxy": 19.5e6},
		HaveIdle:   true, HaveBusy: true,
		IdleTTFB: 500 * time.Millisecond, BusyTTFB: 530 * time.Millisecond,
	})
	if calm.Code != "at_cap" {
		t.Fatalf("calm at_cap = %+v", calm)
	}
}

func TestPeakDown(t *testing.T) {
	samples := []Sample{
		{Outbounds: map[string]TagRate{"proxy": {Down: 1}}, Demand: 1},
		{Outbounds: map[string]TagRate{"proxy": {Down: 19e6}}, Demand: 100},
		{Outbounds: map[string]TagRate{"proxy": {Down: 4}}, Demand: 50},
	}
	if PeakDown(samples) != 19e6 || PeakDemand(samples) != 100 {
		t.Fatalf("peak down %v demand %v", PeakDown(samples), PeakDemand(samples))
	}
}
