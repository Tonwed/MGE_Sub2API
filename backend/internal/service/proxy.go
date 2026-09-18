package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	FallbackModeNone   = "none"
	FallbackModeProxy  = "proxy"
	FallbackModeDirect = "direct"
)

type Proxy struct {
	ID                int64
	Name              string
	Protocol          string
	Host              string
	Port              int
	Username          string
	Password          string
	Status            string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ExpiresAt         *time.Time
	FallbackMode      string
	BackupProxyID     *int64
	ChainProxyID      *int64
	ChainProxy        *Proxy
	ChainProxyChanged bool `json:"-"`
	ExpiryWarnDays    int
}

type ProxyChainHop struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

const proxyChainFragmentPrefix = "sub2api-chain="

func (p *Proxy) IsActive() bool {
	return p.Status == StatusActive
}

// IsExpired 报告代理是否已过期（基于 expires_at，与 status 无关）。
func (p *Proxy) IsExpired(now time.Time) bool {
	return p.ExpiresAt != nil && !p.ExpiresAt.After(now)
}

func (p *Proxy) URL() string {
	u := &url.URL{
		Scheme: p.Protocol,
		Host:   net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
	}
	if p.Username != "" && p.Password != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	if hops := p.ChainHops(); len(hops) > 0 {
		if encoded, err := encodeProxyChainHops(hops); err == nil {
			u.Fragment = proxyChainFragmentPrefix + encoded
		}
	}
	return u.String()
}

func (p *Proxy) ChainHops() []ProxyChainHop {
	if p == nil || p.ChainProxy == nil {
		return nil
	}
	hops := make([]ProxyChainHop, 0, 1)
	seen := make(map[int64]struct{})
	for current := p.ChainProxy; current != nil; current = current.ChainProxy {
		if current.ID > 0 {
			if _, ok := seen[current.ID]; ok {
				break
			}
			seen[current.ID] = struct{}{}
		}
		hops = append(hops, ProxyChainHop{
			Protocol: current.Protocol,
			Host:     current.Host,
			Port:     current.Port,
			Username: current.Username,
			Password: current.Password,
		})
	}
	return hops
}

func ParseProxyChainURL(raw string) (string, []ProxyChainHop, error) {
	raw = strings.TrimSpace(raw)
	index := strings.Index(raw, "#"+proxyChainFragmentPrefix)
	if index < 0 {
		return raw, nil, nil
	}
	encoded := strings.TrimPrefix(raw[index+1:], proxyChainFragmentPrefix)
	clean := raw[:index]
	if encoded == "" {
		return clean, nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, fmt.Errorf("decode proxy chain: %w", err)
	}
	var hops []ProxyChainHop
	if err := json.Unmarshal(payload, &hops); err != nil {
		return "", nil, fmt.Errorf("parse proxy chain: %w", err)
	}
	return clean, hops, nil
}

func encodeProxyChainHops(hops []ProxyChainHop) (string, error) {
	payload, err := json.Marshal(hops)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

type ProxyWithAccountCount struct {
	Proxy
	AccountCount   int64
	LatencyMs      *int64
	LatencyStatus  string
	LatencyMessage string
	IPAddress      string
	Country        string
	CountryCode    string
	Region         string
	City           string
	QualityStatus  string
	QualityScore   *int
	QualityGrade   string
	QualitySummary string
	QualityChecked *int64
}

type ProxyAccountSummary struct {
	ID       int64
	Name     string
	Platform string
	Type     string
	Notes    *string
}
