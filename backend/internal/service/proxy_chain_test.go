package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProxyURLEncodesAndParsesChain(t *testing.T) {
	relay := &Proxy{
		ID:       2,
		Protocol: "socks5h",
		Host:     "relay.example",
		Port:     1080,
		Username: "relay-user",
		Password: "relay-pass",
	}
	target := &Proxy{
		ID:           1,
		Protocol:     "https",
		Host:         "target.example",
		Port:         443,
		Username:     "target-user",
		Password:     "target-pass",
		ChainProxyID: &relay.ID,
		ChainProxy:   relay,
	}

	clean, hops, err := ParseProxyChainURL(target.URL())
	require.NoError(t, err)
	require.Equal(t, "https://target-user:target-pass@target.example:443", clean)
	require.Equal(t, []ProxyChainHop{{
		Protocol: "socks5h",
		Host:     "relay.example",
		Port:     1080,
		Username: "relay-user",
		Password: "relay-pass",
	}}, hops)
}
