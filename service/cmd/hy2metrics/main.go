// Command hy2metrics samples the running xray StatsService, probes 2080,
// and logs Brutal cap recommendations. It does not reload the core and
// does not change server bandwidth.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	statscommand "github.com/v2fly/v2ray-core/v5/app/stats/command"
	"github.com/v2rayA/v2rayA/kernel/metrics"
	"golang.org/x/net/proxy"
	"google.golang.org/grpc"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	base := filepath.Join(home, "Library", "Application Support", "v2raya")
	runtime := flag.String("runtime", filepath.Join(base, "config.runtime.json"), "xray runtime config")
	statusPath := flag.String("status", filepath.Join(base, "metrics-status.json"), "latest sample")
	logPath := flag.String("log", filepath.Join(base, "metrics-decisions.log"), "recommendation log")
	socks := flag.String("socks", "127.0.0.1:2080", "socks5 address for the probe")
	flag.Parse()

	state := loopState{
		capDown: 160 * metrics.MbpsToBps,
		capUp:   40 * metrics.MbpsToBps,
	}
	var counter metrics.Counter
	var ring metrics.Ring
	var sched metrics.Probe
	var lastProbe probeView
	var dial *grpc.ClientConn
	var apiPort int
	defer func() {
		if dial != nil {
			_ = dial.Close()
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			port, portErr := readAPIPort(*runtime)
			if portErr != nil {
				writeStatus(*statusPath, statusDoc{Time: now, Error: portErr.Error(), Decision: metrics.Decision{Action: "hold", Reason: "no_api"}})
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
					writeStatus(*statusPath, statusDoc{Time: now, Error: dialErr.Error(), Decision: metrics.Decision{Action: "hold", Reason: "no_api"}})
					continue
				}
				dial = next
				apiPort = port
			}
			stats, qerr := queryStats(ctx, dial)
			if qerr != nil {
				_ = dial.Close()
				dial = nil
				writeStatus(*statusPath, statusDoc{Time: now, Error: qerr.Error(), Decision: metrics.Decision{Action: "hold", Reason: "stats"}})
				continue
			}
			sample := counter.Sample(stats, now)
			ring.Add(sample)
			if sched.Due(now) {
				ok, ttfb := probeGenerate204(*socks)
				res := sched.Observe(now, ok)
				lastProbe = probeView{OK: ok, TTFBms: ttfb.Milliseconds(), Fails: res.Fails, Failover: res.Failover}
				if res.Failover {
					note := fmt.Sprintf("%s failover recommend tag=proxy fails=%d\n", now.Format(time.RFC3339), res.Fails)
					appendLine(*logPath, note)
				}
			}
			in := controllerInput(state, ring)
			decision := metrics.Evaluate(in)
			if decision.Action == "raise" || decision.Action == "lower" || decision.Action == "revert" {
				appendLine(*logPath, fmt.Sprintf("%s %s %s down %.0f -> %.0f B/s\n",
					now.Format(time.RFC3339), decision.Action, decision.Reason, state.capDown, decision.NextDownBps))
				state.prevDown, state.prevUp = state.capDown, state.capUp
				state.baseline = in.Down10
				state.lastAction = decision.Action
				state.changed = now
			}
			writeStatus(*statusPath, statusDoc{
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

func controllerInput(st loopState, ring metrics.Ring) metrics.Input {
	since := 24 * time.Hour
	if !st.changed.IsZero() {
		since = time.Since(st.changed)
	}
	return metrics.Input{
		CapDown: st.capDown, CapUp: st.capUp,
		PrevCapDown: st.prevDown, PrevCapUp: st.prevUp,
		Down10:       metrics.MeanDown(ring.Last(10), "proxy"),
		Down30:       metrics.MeanDown(ring.Last(30), "proxy"),
		Demand10:     metrics.MeanDemand(ring.Last(10)),
		Samples:      ring.Len(),
		SinceChange:  since,
		LastAction:   st.lastAction,
		BaselineDown: st.baseline,
	}
}

type probeView struct {
	OK       bool  `json:"ok"`
	TTFBms   int64 `json:"ttfb_ms"`
	Fails    int   `json:"fails"`
	Failover bool  `json:"failover"`
}

type statusDoc struct {
	Time         time.Time                  `json:"time"`
	ProxyDownBps float64                    `json:"proxy_down_bps"`
	ProxyUpBps   float64                    `json:"proxy_up_bps"`
	DemandBps    float64                    `json:"demand_bps"`
	DownBps10s   float64                    `json:"down_bps_10s"`
	CapDownBps   float64                    `json:"cap_down_bps"`
	CapUpBps     float64                    `json:"cap_up_bps"`
	Probe        probeView                  `json:"probe"`
	Decision     metrics.Decision           `json:"decision"`
	Outbounds    map[string]metrics.TagRate `json:"outbounds,omitempty"`
	Error        string                     `json:"error,omitempty"`
}

func readAPIPort(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var cfg struct {
		Inbounds []struct {
			Tag  string `json:"tag"`
			Port int    `json:"port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return 0, err
	}
	for _, ib := range cfg.Inbounds {
		if ib.Tag == "api-in_ipv4" && ib.Port > 0 {
			return ib.Port, nil
		}
	}
	return 0, fmt.Errorf("api-in_ipv4 missing in %s", path)
}

func queryStats(ctx context.Context, conn *grpc.ClientConn) ([]metrics.Stat, error) {
	qctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	var response statscommand.QueryStatsResponse
	err := conn.Invoke(qctx, "/xray.app.stats.command.StatsService/QueryStats",
		&statscommand.QueryStatsRequest{Pattern: "", Reset_: false}, &response)
	if err != nil {
		return nil, err
	}
	out := make([]metrics.Stat, 0, len(response.Stat))
	for _, st := range response.Stat {
		out = append(out, metrics.Stat{Name: st.GetName(), Value: st.GetValue()})
	}
	return out, nil
}

func probeGenerate204(socks string) (bool, time.Duration) {
	dialer, err := proxy.SOCKS5("tcp", socks, nil, &net.Dialer{Timeout: 5 * time.Second})
	if err != nil {
		return false, 0
	}
	ctxDial, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return false, 0
	}
	client := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{
		DialContext: ctxDial.DialContext,
	}}
	start := time.Now()
	resp, err := client.Get("https://www.gstatic.com/generate_204")
	if err != nil {
		return false, time.Since(start)
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent, time.Since(start)
}

func writeStatus(path string, doc statusDoc) {
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
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}
