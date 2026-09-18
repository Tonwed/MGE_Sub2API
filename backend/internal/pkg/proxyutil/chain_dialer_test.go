package proxyutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewChainDialerRejectsInvalidHop(t *testing.T) {
	_, err := NewChainDialer([]ChainHop{{
		Protocol: "http",
		Host:     "",
		Port:     8080,
	}})
	require.Error(t, err)

	_, err = NewChainDialer([]ChainHop{{
		Protocol: "unknown",
		Host:     "relay.example",
		Port:     8080,
	}})
	require.Error(t, err)
}
