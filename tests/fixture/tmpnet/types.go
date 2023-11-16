package testnet

import (
	"github.com/ava-labs/avalanchego/genesis"
	"github.com/ava-labs/avalanchego/ids"
)

type CreateNetwork struct {
	Spec   NetworkSpec
	Status NetworkStatus
}

func (cn *CreateNetwork) Start() error {
	return nil
}

type NetworkSpec struct {
	NetworkID uint32

	// Number of keys to pre-fund
	PreFundedKeyCount int

	Genesis *genesis.UnparsedConfig

	ChainConfigs []NamedFlagMap

	FlagSets []NamedFlagMap

	NodeTypes []NodeType

	NodeSets []NodeSet

	Subnets []Subnet
}

type NamedFlagMap struct {
	Name  string
	Flags FlagsMap
}

type NodeType struct {
	Name            string
	FlagSet         string
	Flags           FlagsMap
	LocalNodeConfig *LocalNodeConfig
}

type LocalNodeConfig struct {
	AvalancheGoPath string
}

type NodeSet struct {
	Name      string
	NodeType  string
	FlagSet   string
	Flags     FlagsMap
	NodeCount int
	// The names of the subnets to validate
	ValidatingSubnets []string
}

type Subnet struct {
	Name         string
	SubnetConfig string
	Blockchains  []Blockchain
}

type Blockchain struct {
	VMName  string
	Genesis string
	Config  string
}

type NetworkStatus struct {
	PreFundedKeys []string
	Genesis       *genesis.UnparsedConfig
	NodeSets      []CreatedNodeSet
	Subnets       []CreatedSubnet
}

type CreatedNodeSet struct {
	Name    string
	NodeIDs []ids.ID
}

type CreatedSubnet struct {
	Name        string
	SubnetID    ids.ID
	Blockchains []CreatedBlockchain
}

type CreatedBlockchain struct {
	VMName       string
	VMID         ids.ID
	BlockchainID ids.ID
}

type CreateSubnet struct {
}
