package metrics

import "time"

// AtCapRatio is peak downlink divided by the send cap. A 40MiB pull on
// 2026-10-03 reached 19.5 MB/s against a 20.0 MB/s cap, so 0.85 means the
// ceiling is what stopped the transfer. Protocol overhead keeps the ratio
// a little under 1.
const AtCapRatio = 0.85

// Diagnosis is what the page leads with. Codes:
//
//	warming    fewer than three samples
//	dead       the probe asked for a failover
//	misroute   some outbound other than proxy is carrying the bytes
//	overshoot  delay during a transfer is far above the idle delay
//	idle       no bytes in the short window
//	at_cap     the short window reached the send cap
//	under_cap  bytes are flowing and the cap is not the limit
type Diagnosis struct {
	Code        string  `json:"code"`
	PeakBps     float64 `json:"peak_bps"`
	CapRatio    float64 `json:"cap_ratio"`
	HeadroomBps float64 `json:"headroom_bps"`
	ActiveTag   string  `json:"active_tag,omitempty"`
	IdleTTFBms  int64   `json:"idle_ttfb_ms"`
	BusyTTFBms  int64   `json:"busy_ttfb_ms"`
}

// DiagInput is one diagnosis. LatestDown is the newest one-second rate per tag.
// IdleTTFB is a probe taken while nothing was flowing. BusyTTFB is a probe
// taken during a transfer. The caller drops a busy sample that is no longer recent.
type DiagInput struct {
	CapDown    float64
	PeakDown   float64
	DemandPeak float64
	LatestDown map[string]float64
	Samples    int
	ProbeDead  bool
	IdleTTFB   time.Duration
	BusyTTFB   time.Duration
	HaveIdle   bool
	HaveBusy   bool
}

// Diagnose names the limit the short window can actually see.
func Diagnose(in DiagInput) Diagnosis {
	d := Diagnosis{
		Code:       "warming",
		PeakBps:    in.PeakDown,
		ActiveTag:  hottest(in.LatestDown),
		IdleTTFBms: millis(in.IdleTTFB),
		BusyTTFBms: millis(in.BusyTTFB),
	}
	if in.CapDown > 0 {
		d.CapRatio = in.PeakDown / in.CapDown
		if room := in.CapDown - in.PeakDown; room > 0 {
			d.HeadroomBps = room
		}
	}
	if in.Samples < 3 && !in.ProbeDead {
		return d
	}
	if in.ProbeDead {
		d.Code = "dead"
		return d
	}
	if tag := d.ActiveTag; tag != "" && tag != "proxy" && in.LatestDown[tag] >= DemandFloorBps && in.LatestDown[tag] > in.LatestDown["proxy"] {
		d.Code = "misroute"
		return d
	}
	if overshoot(in) {
		d.Code = "overshoot"
		return d
	}
	if in.PeakDown < DemandFloorBps && in.DemandPeak < DemandFloorBps {
		d.Code = "idle"
		return d
	}
	if in.CapDown > 0 && d.CapRatio >= AtCapRatio {
		d.Code = "at_cap"
		return d
	}
	d.Code = "under_cap"
	return d
}

func overshoot(in DiagInput) bool {
	if !in.HaveIdle || !in.HaveBusy || in.IdleTTFB <= 0 {
		return false
	}
	if in.PeakDown < DemandFloorBps && in.DemandPeak < DemandFloorBps {
		return false
	}
	return in.BusyTTFB > 2*in.IdleTTFB && in.BusyTTFB > in.IdleTTFB+400*time.Millisecond
}

func hottest(rates map[string]float64) string {
	var tag string
	var best float64
	for name, rate := range rates {
		if rate > best {
			tag, best = name, rate
		}
	}
	return tag
}

func millis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}

// PeakDown is the largest one-second proxy downlink in samples.
func PeakDown(samples []Sample) float64 {
	var peak float64
	for _, s := range samples {
		if s.Outbounds["proxy"].Down > peak {
			peak = s.Outbounds["proxy"].Down
		}
	}
	return peak
}

// PeakDemand is the largest one-second socks downlink in samples.
func PeakDemand(samples []Sample) float64 {
	var peak float64
	for _, s := range samples {
		if s.Demand > peak {
			peak = s.Demand
		}
	}
	return peak
}
