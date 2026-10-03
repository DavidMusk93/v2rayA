package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	statscommand "github.com/v2fly/v2ray-core/v5/app/stats/command"
	"golang.org/x/net/proxy"
	"google.golang.org/grpc"
)

// Report is one published sample. Recommendations are not applied.
type Report struct {
	Time         time.Time          `json:"time"`
	ProxyDownBps float64            `json:"proxy_down_bps"`
	ProxyUpBps   float64            `json:"proxy_up_bps"`
	DemandBps    float64            `json:"demand_bps"`
	DownBps10s   float64            `json:"down_bps_10s"`
	CapDownBps   float64            `json:"cap_down_bps"`
	CapUpBps     float64            `json:"cap_up_bps"`
	Probe        ProbeView          `json:"probe"`
	Decision     Decision           `json:"decision"`
	Diagnosis    Diagnosis          `json:"diagnosis"`
	Outbounds    map[string]TagRate `json:"outbounds,omitempty"`
	Error        string             `json:"error,omitempty"`
}

// ProbeView is the last generate_204 attempt.
type ProbeView struct {
	OK       bool  `json:"ok"`
	TTFBms   int64 `json:"ttfb_ms"`
	Fails    int   `json:"fails"`
	Failover bool  `json:"failover"`
}

// Options configures the sampler. APIPort returns 0 while the core is down.
type Options struct {
	APIPort    func() int
	SocksAddr  func() string
	StatusPath string
	LogPath    string
	CapDown    float64
	CapUp      float64
}

var (
	reportMu sync.RWMutex
	current  Report
)

// Current is the latest report. The zero value is "warming" before the first tick.
func Current() Report {
	reportMu.RLock()
	defer reportMu.RUnlock()
	if current.Decision.Action == "" {
		return Report{Decision: Decision{Action: "hold", Reason: "warming"}}
	}
	return current
}

func publish(r Report) {
	reportMu.Lock()
	current = r
	reportMu.Unlock()
	if r.Time.IsZero() {
		return
	}
	writeStatus(currentPath, r)
}

var currentPath string

// Run samples until ctx is cancelled. It is safe to call once.
func Run(ctx context.Context, opt Options) {
	if opt.APIPort == nil {
		opt.APIPort = func() int { return 0 }
	}
	if opt.SocksAddr == nil {
		opt.SocksAddr = func() string { return "127.0.0.1:2080" }
	}
	if opt.CapDown <= 0 {
		opt.CapDown = 160 * MbpsToBps
	}
	if opt.CapUp <= 0 {
		opt.CapUp = 40 * MbpsToBps
	}
	currentPath = opt.StatusPath
	state := loopState{capDown: opt.CapDown, capUp: opt.CapUp}
	var counter Counter
	var ring Ring
	var sched Probe
	var lastProbe ProbeView
	var busy busyProbe
	var idleTTFB time.Duration
	var haveIdle bool
	var dial *grpc.ClientConn
	var apiPort int
	defer func() {
		if dial != nil {
			_ = dial.Close()
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			port := opt.APIPort()
			if port <= 0 {
				publish(Report{Time: now, Decision: Decision{Action: "hold", Reason: "no_api"}, CapDownBps: state.capDown, CapUpBps: state.capUp})
				continue
			}
			if dial == nil || port != apiPort {
				if dial != nil {
					_ = dial.Close()
				}
				dialCtx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
				next, dialErr := grpc.DialContext(dialCtx, net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), grpc.WithInsecure(), grpc.WithBlock())
				cancel()
				if dialErr != nil {
					dial = nil
					publish(Report{Time: now, Error: dialErr.Error(), Decision: Decision{Action: "hold", Reason: "no_api"}, CapDownBps: state.capDown, CapUpBps: state.capUp})
					continue
				}
				dial = next
				apiPort = port
			}
			stats, qerr := queryStats(ctx, dial)
			if qerr != nil {
				_ = dial.Close()
				dial = nil
				publish(Report{Time: now, Error: qerr.Error(), Decision: Decision{Action: "hold", Reason: "stats"}, CapDownBps: state.capDown, CapUpBps: state.capUp})
				continue
			}
			sample := counter.Sample(stats, now)
			ring.Add(sample)
			busyNow := sample.Demand >= DemandFloorBps || sample.Outbounds["proxy"].Down >= DemandFloorBps
			probed := false
			if sched.Due(now) {
				probed = true
				ok, ttfb := probeGenerate204(opt.SocksAddr())
				res := sched.Observe(now, ok)
				lastProbe = ProbeView{OK: ok, TTFBms: ttfb.Milliseconds(), Fails: res.Fails, Failover: res.Failover}
				if ok && busyNow {
					busy.note(ttfb, now)
				} else if ok {
					idleTTFB = ttfb
					haveIdle = true
				}
				if res.Failover {
					appendLine(opt.LogPath, fmt.Sprintf("%s failover recommend tag=proxy fails=%d\n", now.Format(time.RFC3339), res.Fails))
				}
			}
			if busyNow && !probed && busy.begin(now) {
				socks := opt.SocksAddr()
				go func() {
					ok, ttfb := probeGenerate204(socks)
					if ok {
						busy.note(ttfb, time.Now())
					}
					busy.done()
				}()
			}
			in := controllerInput(state, ring)
			decision := Evaluate(in)
			window := ring.Last(10)
			latest := map[string]float64{}
			for tag, rate := range sample.Outbounds {
				latest[tag] = rate.Down
			}
			busyTTFB, haveBusy := busy.recent(now)
			diag := Diagnose(DiagInput{
				CapDown:    state.capDown,
				PeakDown:   PeakDown(window),
				DemandPeak: PeakDemand(window),
				LatestDown: latest,
				Samples:    ring.Len(),
				ProbeDead:  lastProbe.Failover,
				IdleTTFB:   idleTTFB,
				BusyTTFB:   busyTTFB,
				HaveIdle:   haveIdle,
				HaveBusy:   haveBusy,
			})
			if diag.Code != state.lastDiag && (diag.Code == "at_cap" || diag.Code == "overshoot" || diag.Code == "dead" || diag.Code == "misroute") {
				appendLine(opt.LogPath, fmt.Sprintf("%s diagnose %s peak %.0f B/s idle %dms busy %dms tag %s\n",
					now.Format(time.RFC3339), diag.Code, diag.PeakBps, diag.IdleTTFBms, diag.BusyTTFBms, diag.ActiveTag))
				state.lastDiag = diag.Code
			}
			if decision.Action == "raise" || decision.Action == "lower" || decision.Action == "revert" {
				appendLine(opt.LogPath, fmt.Sprintf("%s %s %s down %.0f -> %.0f B/s\n",
					now.Format(time.RFC3339), decision.Action, decision.Reason, state.capDown, decision.NextDownBps))
				state.prevDown, state.prevUp = state.capDown, state.capUp
				state.baseline = in.Down10
				state.lastAction = decision.Action
				state.changed = now
			}
			publish(Report{
				Time:         now,
				ProxyDownBps: sample.Outbounds["proxy"].Down,
				ProxyUpBps:   sample.Outbounds["proxy"].Up,
				DemandBps:    sample.Demand,
				DownBps10s:   in.Down10,
				CapDownBps:   state.capDown,
				CapUpBps:     state.capUp,
				Probe:        lastProbe,
				Decision:     decision,
				Diagnosis:    diag,
				Outbounds:    sample.Outbounds,
			})
		}
	}
}

type loopState struct {
	capDown, capUp   float64
	prevDown, prevUp float64
	baseline         float64
	lastAction       string
	changed          time.Time
	lastDiag         string
}

// busyProbe measures generate_204 while bytes are flowing, without
// stalling the one-second counter loop. A sample older than 20s is ignored.
type busyProbe struct {
	mu       sync.Mutex
	inflight bool
	last     time.Time
	ttfb     time.Duration
	have     bool
	at       time.Time
}

func (b *busyProbe) begin(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.inflight || now.Sub(b.last) < 8*time.Second {
		return false
	}
	b.inflight = true
	b.last = now
	return true
}

func (b *busyProbe) done() {
	b.mu.Lock()
	b.inflight = false
	b.mu.Unlock()
}

func (b *busyProbe) note(ttfb time.Duration, at time.Time) {
	b.mu.Lock()
	b.ttfb = ttfb
	b.have = true
	b.at = at
	b.mu.Unlock()
}

func (b *busyProbe) recent(now time.Time) (time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.have || now.Sub(b.at) > 20*time.Second {
		return 0, false
	}
	return b.ttfb, true
}

func controllerInput(st loopState, ring Ring) Input {
	since := 24 * time.Hour
	if !st.changed.IsZero() {
		since = time.Since(st.changed)
	}
	return Input{
		CapDown: st.capDown, CapUp: st.capUp,
		PrevCapDown: st.prevDown, PrevCapUp: st.prevUp,
		Down10:       MeanDown(ring.Last(10), "proxy"),
		Down30:       MeanDown(ring.Last(30), "proxy"),
		Demand10:     MeanDemand(ring.Last(10)),
		Samples:      ring.Len(),
		SinceChange:  since,
		LastAction:   st.lastAction,
		BaselineDown: st.baseline,
	}
}

func queryStats(ctx context.Context, conn *grpc.ClientConn) ([]Stat, error) {
	qctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	var response statscommand.QueryStatsResponse
	err := conn.Invoke(qctx, "/xray.app.stats.command.StatsService/QueryStats",
		&statscommand.QueryStatsRequest{Pattern: "", Reset_: false}, &response)
	if err != nil {
		return nil, err
	}
	out := make([]Stat, 0, len(response.Stat))
	for _, st := range response.Stat {
		out = append(out, Stat{Name: st.GetName(), Value: st.GetValue()})
	}
	return out, nil
}

func probeGenerate204(socks string) (bool, time.Duration) {
	if socks == "" {
		return false, 0
	}
	dialer, err := proxy.SOCKS5("tcp", socks, nil, &net.Dialer{Timeout: 5 * time.Second})
	if err != nil {
		return false, 0
	}
	ctxDial, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return false, 0
	}
	client := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{DialContext: ctxDial.DialContext}}
	start := time.Now()
	resp, err := client.Get("https://www.gstatic.com/generate_204")
	if err != nil {
		return false, time.Since(start)
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent, time.Since(start)
}

func writeStatus(path string, doc Report) {
	if path == "" {
		return
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func appendLine(path, line string) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}
