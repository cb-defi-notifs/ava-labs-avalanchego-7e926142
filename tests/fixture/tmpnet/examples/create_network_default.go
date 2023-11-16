package testnet

import (
	"github.com/ava-labs/avalanchego/config"
	"github.com/ava-labs/avalanchego/tests/fixture/testnet"
)

func DefaultNetworkSpec(avalancheGoExecPath string) *testnet.NetworkSpec {
	return &testnet.NetworkSpec{
		PreFundedKeyCount: testnet.DefaultFundedKeyCount,
		NodeTypes: []testnet.NodeType{
			{
				Name: "local",
				LocalNodeConfig: &testnet.LocalNodeConfig{
					AvalancheGoPath: avalancheGoExecPath,
				},
			},
		},
		NodeSets: []testnet.NodeSet{
			{
				Name:      "local-primary",
				NodeType:  "local",
				NodeCount: testnet.DefaultNodeCount,
				Subnets: []string{
					testnet.PrimarySubnet,
				},
			},
		},
	}
}

func CreateDefaultNetwork(avalancheGoExecPath) {
	networkSpec := DefaultNetworkSpec("/path/to/avalanchego")
	network, err := StartNetwork(networkSpec)
	if err != nil {
		panic(err)
	}
}

func StartNetwork(spec *testnet.NetworkSpec) error {
	// Determine primary validators
	// Generate pre-funded keys
	// Generate genesis with validators and keys
}
