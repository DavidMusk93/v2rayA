// Package metrics turns xray StatsService counters into per-outbound rates
// and decides whether a fixed Hysteria2 Brutal cap should move.
// Decisions are recommendations. Applying one reloads the core.
package metrics

import (
	"strings"
	"time"
)

// Stat is one xray stats counter. Names look like
// outbound>>>proxy>>>traffic>>>downlink.
type Stat struct {
	Name  string
	Value int64
}

// TagRate is one outbound or the summed inbound demand over one interval.
type TagRate struct {
	Up        float64 `json:"up"`
	Down      float64 `json:"down"`
	UpTotal   int64   `json:"upTotal"`
	DownTotal int64   `json:"downTotal"`
}

// Sample is one one-second observation.
// Up/Down stay the sum of proxy outbounds so the existing traffic feed
// keeps its shape. Outbounds breaks that sum apart. Demand is inbound
// downlink on socks_* (bytes the proxy wrote toward the client).
type Sample struct {
	Up        float64            `json:"up"`
	Down      float64            `json:"down"`
	UpTotal   int64              `json:"upTotal"`
	DownTotal int64              `json:"downTotal"`
	Demand    float64            `json:"demand,omitempty"`
	Outbounds map[string]TagRate `json:"outbounds,omitempty"`
	At        time.Time          `json:"at"`
}

type tagTotals struct {
	up, down int64
}

// Counter keeps the previous absolute counters so the next sample can
// emit bytes per second. A drop means the core restarted; that interval
// contributes no rate.
type Counter struct {
	out map[string]tagTotals
	in  map[string]int64
	at  time.Time
}

func (c *Counter) Sample(stats []Stat, now time.Time) Sample {
	out := map[string]tagTotals{}
	in := map[string]int64{}
	for _, stat := range stats {
		kind, tag, direction, ok := parseStat(stat.Name)
		if !ok {
			continue
		}
		switch kind {
		case "outbound":
			if infraOutbound(tag) {
				continue
			}
			cur := out[tag]
			switch direction {
			case "uplink":
				cur.up = stat.Value
			case "downlink":
				cur.down = stat.Value
			default:
				continue
			}
			out[tag] = cur
		case "inbound":
			if direction != "downlink" || !strings.HasPrefix(tag, "socks") {
				continue
			}
			in[tag] = stat.Value
		}
	}

	elapsed := now.Sub(c.at).Seconds()
	rate := !c.at.IsZero() && elapsed > 0
	sample := Sample{At: now, Outbounds: map[string]TagRate{}}
	for tag, cur := range out {
		tr := TagRate{UpTotal: cur.up, DownTotal: cur.down}
		if rate {
			if prev, ok := c.out[tag]; ok && cur.up >= prev.up {
				tr.Up = float64(cur.up-prev.up) / elapsed
			}
			if prev, ok := c.out[tag]; ok && cur.down >= prev.down {
				tr.Down = float64(cur.down-prev.down) / elapsed
			}
		}
		sample.Outbounds[tag] = tr
		sample.UpTotal += cur.up
		sample.DownTotal += cur.down
		sample.Up += tr.Up
		sample.Down += tr.Down
	}
	var demandTotal int64
	var demandPrev int64
	var demandOK = rate
	for tag, cur := range in {
		demandTotal += cur
		prev, ok := c.in[tag]
		if !ok || cur < prev {
			demandOK = false
		} else {
			demandPrev += prev
		}
	}
	if demandOK && elapsed > 0 {
		sample.Demand = float64(demandTotal-demandPrev) / elapsed
	}
	c.out, c.in, c.at = out, in, now
	return sample
}

func parseStat(name string) (kind, tag, direction string, ok bool) {
	kind, rest, ok := strings.Cut(name, ">>>")
	if !ok || (kind != "outbound" && kind != "inbound") {
		return "", "", "", false
	}
	tag, direction, ok = strings.Cut(rest, ">>>traffic>>>")
	if !ok || tag == "" || direction == "" {
		return "", "", "", false
	}
	return kind, tag, direction, true
}

func infraOutbound(tag string) bool {
	switch tag {
	case "direct", "block", "dns-out", "api-out":
		return true
	default:
		return false
	}
}
