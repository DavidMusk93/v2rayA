package serverObj

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/v2rayA/v2rayA/kernel/coreObj"
)

func init() {
	FromLinkRegister("hysteria2", NewHysteria2)
	FromLinkRegister("hy2", NewHysteria2)
	EmptyRegister("hysteria2", func() (ServerObj, error) {
		return new(Hysteria2), nil
	})
	EmptyRegister("hy2", func() (ServerObj, error) {
		return new(Hysteria2), nil
	})
}

type Hysteria2 struct {
	Name     string `json:"name"`
	Server   string `json:"server"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Link     string `json:"link"`
}

func NewHysteria2(link string) (ServerObj, error) {
	return ParseHysteria2URL(link)
}

func ParseHysteria2URL(link string) (data *Hysteria2, err error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	portStr := u.Port()
	if portStr == "" {
		portStr = "443"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}
	return &Hysteria2{
		Name:     u.Fragment,
		Server:   u.Hostname(),
		Port:     port,
		Protocol: "hysteria2",
		Link:     link,
	}, nil
}

// hysteria2ClientSettings is the outbound "settings" JSON of xray's native
// hysteria protocol (version 2, i.e. hysteria2).
type hysteria2ClientSettings struct {
	Version int32  `json:"version"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// hysteria2Params holds the parameters extracted from a hysteria2:// link.
type hysteria2Params struct {
	password             string
	sni                  string
	pinnedPeerCertSha256 string
	verifyPeerCertByName string
	obfs                 string
	obfsPassword         string
	alpn                 []string
	// upMbps / downMbps are decimal megabits from the share link.
	// upMbps is this client's send ceiling. downMbps is the receive
	// ceiling advertised in Hysteria-CC-RX; the server paces its send
	// at min(server up_mbps, this value). xray's "N mbps" unit is
	// mebibit/s (N * 1024 * 1024 / 8 bytes), about 4.9% above decimal.
	upMbps   int
	downMbps int
}

// parseLinkParams extracts the hysteria2 URL parameters, which are the source
// of the transport config (password, sni, certificate pinning, salamander
// obfs). Both the standard hysteria2 client parameter names
// (pinSHA256/pinSha256) and v2rayA's legacy names (pinned_peer_cert_sha256)
// are accepted.
func (s *Hysteria2) parseLinkParams() (p hysteria2Params) {
	if s.Link == "" {
		return p
	}
	u, err := url.Parse(s.Link)
	if err != nil {
		return p
	}
	if u.User != nil {
		p.password = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			p.password += ":" + pw
		}
	}
	q := u.Query()
	p.sni = q.Get("sni")
	p.pinnedPeerCertSha256 = q.Get("pinSHA256")
	if p.pinnedPeerCertSha256 == "" {
		p.pinnedPeerCertSha256 = q.Get("pinSha256")
	}
	if p.pinnedPeerCertSha256 == "" {
		p.pinnedPeerCertSha256 = q.Get("pin_sha256")
	}
	if p.pinnedPeerCertSha256 == "" {
		p.pinnedPeerCertSha256 = q.Get("pinned_peer_cert_sha256")
	}
	p.verifyPeerCertByName = q.Get("verify_peer_cert_by_name")
	p.obfs = q.Get("obfs")
	p.obfsPassword = q.Get("obfs-password")
	for _, part := range strings.Split(q.Get("alpn"), ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			p.alpn = append(p.alpn, part)
		}
	}
	p.upMbps, _ = strconv.Atoi(q.Get("upmbps"))
	p.downMbps, _ = strconv.Atoi(q.Get("downmbps"))
	return p
}

func (s *Hysteria2) Configuration(info PriorInfo) (c Configuration, err error) {
	p := s.parseLinkParams()

	settingsJSON, err := json.Marshal(hysteria2ClientSettings{
		Version: 2,
		Address: s.Server,
		Port:    s.Port,
	})
	if err != nil {
		return c, fmt.Errorf("hysteria2: marshal settings: %w", err)
	}

	pins, err := coreObj.PinnedPeerCertSha256Hex(p.pinnedPeerCertSha256)
	if err != nil {
		return c, fmt.Errorf("hysteria2: %w", err)
	}
	tlsSettings := &coreObj.TLSSettings{
		PinnedPeerCertSha256: pins,
		VerifyPeerCertByName: p.verifyPeerCertByName,
	}
	if p.sni == "" {
		tlsSettings.ServerName = s.Server
	} else {
		tlsSettings.ServerName = p.sni
	}
	if len(p.alpn) > 0 {
		tlsSettings.Alpn = p.alpn
	}

	streamSettings := &coreObj.StreamSettings{
		Network:     "hysteria",
		Security:    "tls",
		TLSSettings: tlsSettings,
		HysteriaSettings: &coreObj.HysteriaSettings{
			Version: 2,
			Auth:    p.password,
		},
	}
	if p.obfs == "salamander" {
		maskSettings, err := json.Marshal(map[string]string{"password": p.obfsPassword})
		if err != nil {
			return c, fmt.Errorf("hysteria2: marshal obfs settings: %w", err)
		}
		streamSettings.FinalMask = &coreObj.FinalMask{
			Udp: []coreObj.UdpMask{{
				Type:     "salamander",
				Settings: maskSettings,
			}},
		}
	}
	if p.upMbps > 0 || p.downMbps > 0 {
		if streamSettings.FinalMask == nil {
			streamSettings.FinalMask = &coreObj.FinalMask{}
		}
		quicParams := &coreObj.QuicParams{
			Congestion: "brutal",
			BbrProfile: "aggressive",
		}
		if p.upMbps > 0 {
			quicParams.BrutalUp = fmt.Sprintf("%d mbps", p.upMbps)
		}
		if p.downMbps > 0 {
			quicParams.BrutalDown = fmt.Sprintf("%d mbps", p.downMbps)
		}
		streamSettings.FinalMask.QuicParams = quicParams
	}

	return Configuration{
		CoreOutbound: coreObj.OutboundObject{
			Tag:            info.Tag,
			Protocol:       "hysteria",
			Settings:       coreObj.Settings{Inlined: settingsJSON},
			StreamSettings: streamSettings,
		},
		UDPSupport: true,
	}, nil
}

func (s *Hysteria2) ExportToURL() string {
	return s.Link
}

func (s *Hysteria2) NeedPluginPort() bool {
	return false
}

func (s *Hysteria2) ProtoToShow() string {
	return fmt.Sprintf("hysteria2")
}

func (s *Hysteria2) GetProtocol() string {
	return s.Protocol
}

func (s *Hysteria2) GetHostname() string {
	return s.Server
}

func (s *Hysteria2) GetPort() int {
	return s.Port
}

func (s *Hysteria2) GetName() string {
	return s.Name
}

func (s *Hysteria2) SetName(name string) {
	s.Name = name
}
