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
			if sched.Due(now) {
				ok, ttfb := probeGenerate204(opt.SocksAddr())
				res := sched.Observe(now, ok)
				lastProbe = ProbeView{OK: ok, TTFBms: ttfb.Milliseconds(), Fails: res.Fails, Failover: res.Failover}
				if res.Failover {
					appendLine(opt.LogPath, fmt.Sprintf("%s failover recommend tag=proxy fails=%d\n", now.Format(time.RFC3339), res.Fails))
				}
			}
			in := controllerInput(state, ring)
			decision := Evaluate(in)
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
