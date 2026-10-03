// Command hy2metrics runs the sampler outside v2rayA. The service starts
// the same loop itself; use this only when that process is not running.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/v2rayA/v2rayA/kernel/metrics"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	metrics.Run(ctx, metrics.Options{
		APIPort:    func() int { return portFromRuntime(*runtime) },
		SocksAddr:  func() string { return *socks },
		StatusPath: *statusPath,
		LogPath:    *logPath,
		CapDown:    160 * metrics.MbpsToBps,
		CapUp:      40 * metrics.MbpsToBps,
	})
}

func portFromRuntime(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var cfg struct {
		Inbounds []struct {
			Tag  string `json:"tag"`
			Port int    `json:"port"`
		} `json:"inbounds"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return 0
	}
	for _, ib := range cfg.Inbounds {
		if ib.Tag == "api-in_ipv4" && ib.Port > 0 {
			return ib.Port
		}
	}
	return 0
}
