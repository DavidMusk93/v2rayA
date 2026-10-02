package metrics

import "time"

const (
	// DemandFloorBps ignores quiet intervals. A tunnel with nobody
	// downloading is not a slow tunnel.
	DemandFloorBps = 500_000
	RaiseUtil      = 0.9
	LowerUtil      = 0.7
	// Step is +10% / -10%. The 200 Mbps trial sat ~25% above the path
	// and lost goodput; smaller steps can be reverted from one window.
	Step        = 1.1
	RevertRatio = 0.85
	Settle      = 10 * time.Second
	Sustain     = 30 * time.Second
	// ReloadGap is the minimum time between cap changes. A reload drops
	// the QUIC session; the handshake on this path is about half a second.
	ReloadGap  = 3 * time.Minute
	MinDownBps = 8_000_000
	MaxDownBps = 35_000_000
	MinUpBps   = 1_000_000
	MaxUpBps   = 10_000_000
	// MbpsToBps converts sing-box decimal megabits per second.
	MbpsToBps = 125_000
)

// Input is the controller's view of the last half minute.
// Down10 / Down30 are mean proxy downlink bytes per second.
// Demand10 is mean socks inbound downlink; zero means "use Down10".
type Input struct {
	CapDown      float64
	CapUp        float64
	PrevCapDown  float64
	PrevCapUp    float64
	Down10       float64
	Down30       float64
	Demand10     float64
	Samples      int
	SinceChange  time.Duration
	LastAction   string
	BaselineDown float64
}

// Decision is a recommendation. Nothing in this package reloads xray
// or writes the server inbound.
type Decision struct {
	Action      string  `json:"action"`
	Reason      string  `json:"reason"`
	NextDownBps float64 `json:"next_down_bps"`
	NextUpBps   float64 `json:"next_up_bps"`
	Utilize     float64 `json:"utilization"`
}

// Evaluate applies the Brutal cap rules.
//
// Hold while the window is short or the tunnel is idle.
// Raise 10% when 30s utilization stays above 0.9.
// Lower 10% when it stays under 0.7 while there is demand.
// Revert a raise when the next 10s goodput falls below 85% of the
// pre-raise window. That is the 200 Mbps trial: 15.25 MB/s then 12.10 MB/s.
func Evaluate(in Input) Decision {
	util := 0.0
	if in.CapDown > 0 {
		util = in.Down30 / in.CapDown
	}
	hold := Decision{Action: "hold", Reason: "in_band", NextDownBps: in.CapDown, NextUpBps: in.CapUp, Utilize: util}
	if in.Samples < int(Sustain/time.Second) {
		hold.Reason = "warming"
		return hold
	}
	activity := in.Demand10
	if activity <= 0 {
		activity = in.Down10
	}
	if activity < DemandFloorBps {
		hold.Reason = "idle"
		return hold
	}
	if in.LastAction == "raise" && in.SinceChange >= Settle && in.BaselineDown > 0 && in.Down10 < RevertRatio*in.BaselineDown {
		return Decision{
			Action: "revert", Reason: "goodput_drop",
			NextDownBps: in.PrevCapDown, NextUpBps: in.PrevCapUp, Utilize: util,
		}
	}
	if in.LastAction != "" && in.SinceChange < ReloadGap {
		hold.Reason = "reload_gap"
		return hold
	}
	if util > RaiseUtil {
		next := capRange(in.CapDown*Step, MinDownBps, MaxDownBps)
		up := capRange(in.CapUp*Step, MinUpBps, MaxUpBps)
		if next <= in.CapDown {
			hold.Reason = "cap"
			return hold
		}
		return Decision{Action: "raise", Reason: "utilization", NextDownBps: next, NextUpBps: up, Utilize: util}
	}
	if util < LowerUtil {
		next := capRange(in.CapDown/Step, MinDownBps, MaxDownBps)
		up := capRange(in.CapUp/Step, MinUpBps, MaxUpBps)
		if next >= in.CapDown {
			hold.Reason = "floor"
			return hold
		}
		return Decision{Action: "lower", Reason: "underfill", NextDownBps: next, NextUpBps: up, Utilize: util}
	}
	return hold
}

func capRange(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// MeanDown returns the mean proxy-tag downlink over samples.
// An empty tag uses Sample.Down, which is the sum of proxy outbounds.
func MeanDown(samples []Sample, tag string) float64 {
	return mean(samples, func(s Sample) float64 {
		if tag == "" {
			return s.Down
		}
		return s.Outbounds[tag].Down
	})
}

// MeanDemand returns the mean socks inbound downlink.
func MeanDemand(samples []Sample) float64 {
	return mean(samples, func(s Sample) float64 { return s.Demand })
}

func mean(samples []Sample, value func(Sample) float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		sum += value(s)
	}
	return sum / float64(len(samples))
}
