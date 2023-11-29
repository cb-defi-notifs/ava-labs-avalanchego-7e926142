// Copyright (C) 2019-2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package tmpnet

const (
	// Arbitrary name
	// TODO(marun) Maybe avoid requiring a name where possible?
	defaultName = "default"
)

// A set of flags appropriate for local testing.
func LocalFlags() FlagsMap {
	// Supply only non-default configuration to ensure that default values will be used.
	return FlagsMap{
		config.NetworkPeerListGossipFreqKey: "250ms",
		config.NetworkMaxReconnectDelayKey:  "1s",
		config.PublicIPKey:                  "127.0.0.1",
		config.HTTPHostKey:                  "127.0.0.1",
		config.StakingHostKey:               "127.0.0.1",
		config.HealthCheckFreqKey:           "2s",
		config.AdminAPIEnabledKey:           true,
		config.IpcAPIEnabledKey:             true,
		config.IndexEnabledKey:              true,
		config.LogDisplayLevelKey:           "INFO",
		config.LogLevelKey:                  "DEBUG",
		config.MinStakeDurationKey:          DefaultMinStakeDuration.String(),
	}
}

// C-Chain config for local testing.
func LocalCChainConfig() FlagsMap {
	// Supply only non-default configuration to ensure that default
	// values will be used. Available C-Chain configuration options are
	// defined in the `github.com/ava-labs/coreth/evm` package.
	return FlagsMap{
		"log-level": "trace",
	}
}

type NetworkSpec struct {
	DefaultFlags      FlagsMap
	ChainConfigs      map[string]FlagsMap
	PreFundedKeyCount int
	NodeTypes         []NodeType
	NodeSpecs         []NodeSpecs
}

type LocalNodeType struct {
	AvalancheGoPath string
}

type NodeType struct {
	Name  string
	Local *LocalNodeConfig
}

type NodeSpec struct {
	Name            string
	NodeType        string
	Replicas        int
	IsInitialStaker bool
}

func DefaultNetworkSpec(networkDir string, nodeCount int, avalancheGoPath string) (*NetworkSpec, error) {
	return &NetworkSpec{
		FlagsMap:          LocalFlags(),
		PreFundedKeyCount: DefaultFundedKeyCount,
		PrimaryChainConfigs: map[string]FlagsMap{
			"C": LocalCChainConfig(),
		},
		NodeTypes: []NodeType{
			{
				Name: defaultName,
				Local: &LocalNodeType{
					AvalancheGoPath: avalancheGoPath,
				},
			},
		},
		NodeSpecs: []NodeSpec{
			{
				Name:          defaultName,
				NodeType:      defaultName,
				Replicas:      nodeCount,
				InitialStaker: true,
			},
		},
	}
}
