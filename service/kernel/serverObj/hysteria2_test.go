package serverObj

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHysteria2LinkSetsBrutalAndALPN(t *testing.T) {
	obj, err := ParseHysteria2URL("hysteria2://pw@h.example.com:16420?sni=h.example.com&alpn=h3&upmbps=50&downmbps=200#hy2")
	if err != nil {
		t.Fatal(err)
	}
	config, err := obj.Configuration(PriorInfo{Tag: "proxy"})
	if err != nil {
		t.Fatal(err)
	}
	stream := config.CoreOutbound.StreamSettings
	if stream == nil || stream.FinalMask == nil || stream.FinalMask.QuicParams == nil {
		t.Fatalf("quicParams missing: %+v", stream)
	}
	quic := stream.FinalMask.QuicParams
	if quic.Congestion != "brutal" || quic.BrutalUp != "50 mbps" || quic.BrutalDown != "200 mbps" {
		t.Fatalf("quicParams = %+v", quic)
	}
	if stream.TLSSettings == nil || len(stream.TLSSettings.Alpn) != 1 || stream.TLSSettings.Alpn[0] != "h3" {
		t.Fatalf("alpn = %+v", stream.TLSSettings)
	}
	raw, err := json.Marshal(config.CoreOutbound)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"brutalUp":"50 mbps"`, `"brutalDown":"200 mbps"`, `"alpn":["h3"]`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("outbound JSON missing %s: %s", want, raw)
		}
	}
}

func TestHysteria2WithoutBandwidthLeavesBBR(t *testing.T) {
	obj, err := ParseHysteria2URL("hysteria2://pw@h.example.com:16420?sni=h.example.com")
	if err != nil {
		t.Fatal(err)
	}
	config, err := obj.Configuration(PriorInfo{Tag: "proxy"})
	if err != nil {
		t.Fatal(err)
	}
	stream := config.CoreOutbound.StreamSettings
	if stream.FinalMask != nil {
		t.Fatalf("finalmask = %+v, want none", stream.FinalMask)
	}
}
