package testnet

import (
	"github.com/ava-labs/avalanchego/config"
	"github.com/ava-labs/avalanchego/tests/fixture/testnet"
)

func CreateNetworkWithSubnets() {
	xsvmBlockchain := testnet.Blockchain{
		VMName:      "xsvm",
		ChainConfig: "<config contents>",
		Genesis:     "<genesis contents>",
	}

	n := &testnet.CreateNetwork{
		Spec: testnet.NetworkSpec{
			NodeTypes: []testnet.NodeType{
				{
					Name: "local",
					Flags: testnet.FlagsMap{
						config.PluginDirKey: "/path/to/plugins",
					},
					LocalNodeConfig: &testnet.LocalNodeConfig{
						AvalancheGoPath: "/path/to/avalanchego",
					},
				},
			},
			NodeSets: []testnet.NodeSet{
				{
					Name:     "local-subnet1",
					NodeType: "local",
					Subnets: []string{
						// Both initial validators of primary network and validators of subnet1
						"primary",
						"subnet1",
					},
				},
				{
					Name:     "local-subnet2",
					NodeType: "local",
					Subnets: []string{
						// Only validators of subnet2
						"subnet2",
					},
				},
			},
			Subnets: []testnet.Subnet{
				{
					Name: "subnet1",
					Blockchains: []testnet.Blockchain{
						xsvmBlockchain,
					},
				},
				{
					Name: "subnet2",
					Blockchains: []testnet.Blockchain{
						xsvmBlockchain,
					},
				},
			},
		},
	}

	// Write configuratino to disk in prepraration for start
	if err := n.Start(); err != nil {
		panic(err)
	}
}
