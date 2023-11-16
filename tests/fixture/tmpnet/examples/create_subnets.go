package testnet

import (
	"github.com/ava-labs/avalanchego/config"
	"github.com/ava-labs/avalanchego/tests/fixture/testnet"
)

func CreateSubnets() {
	nodeCount := testnet.DefaultNodeCount
	localType := "local"
	flags := testnet.FlagsMap{
		config.PluginDirKey: "/path/to/plugins",
	}
	blockchains := []testnet.Blockchain{
		{
			VMName:      "xsvm",
			ChainConfig: "<config contents>",
			Genesis:     "<genesis contents>",
		},
	}

	n := &testnet.CreateSubnets{
		Spec: testnet.CreateSubnetsSpec{

			// 1000/

			// PrivateKey: secp256k1.PrivateKey
			NodeGenerators: []testnet.NodeGenerator{
				{
					Name: "subnet1-nodes",
					// NodeType:  localType, // NodeType must be defined for target network
					Flags:     flags,
					NodeCount: nodeCount, // Count is explicit
					ValidatedSubnets: []string{
						"subnet1",
					},
				},
				{
					Name:      "subnet2-nodes",
					NodeType:  localType,
					Flags:     flags,
					NodeCount: nodeCount,
					ValidatedSubnets: []string{
						"subnet2",
					},
				},
			},
			Subnets: []testnet.Subnet{
				{
					Name:        "subnet1",
					Blockchains: blockchains,
				},
				{
					Name:        "subnet2",
					Blockchains: blockchains,
				},
			},
		},
	}

	// For each subnet
	//   create subnets
	// Create each node set
	//   for each node
	//     determine its configuration
	//     - Tracked-Subnets > Flags > FlagSet > NodeType.Flags > NodeType.FlagSet
	//     start the node
	// For each subnet
	//  determine the nodes validating the subnet
	//  create blockchains for the subnet
	//  Add nodes for the subnet as primary network validators
	//  add subnet nodes as subnet validators
	// Wait for new nodes to report healthy

	// Starting a node
	//  collect which nodes are running
	//  if any are running, use them as bootstrap nodes
	//  if none are running, use no bootstrap?

	if err := n.Start(); err != nil {
		panic(err)
	}
}
