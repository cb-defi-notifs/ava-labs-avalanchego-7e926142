// Copyright (C) 2019-2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package tmpnet

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	"github.com/ava-labs/avalanchego/config"
	"github.com/ava-labs/avalanchego/genesis"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/crypto/bls"
	"github.com/ava-labs/avalanchego/utils/crypto/secp256k1"
	"github.com/ava-labs/avalanchego/utils/set"
	"github.com/ava-labs/avalanchego/utils/units"
	"github.com/ava-labs/avalanchego/vms/platformvm"
	"github.com/ava-labs/avalanchego/vms/platformvm/reward"
	"github.com/ava-labs/avalanchego/vms/platformvm/signer"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
	"github.com/ava-labs/avalanchego/vms/secp256k1fx"
	"github.com/ava-labs/avalanchego/wallet/chain/p"
	"github.com/ava-labs/avalanchego/wallet/subnet/primary/common"
)

const (
	// TODO(marun) Need to reconcile these constants with those in e2e.go

	DefaultTimeout = 2 * time.Minute

	// Interval appropriate for network operations that should be
	// retried periodically but not too often.
	DefaultPollingInterval = 500 * time.Millisecond

	// Start time must be a minimum of 15s ahead of the current time
	// or validator addition will fail.
	DefaultValidatorStartTimeDiff = 20 * time.Second
)

// Specifies the configuration for a new subnet.
type SubnetSpec struct {
	Name            string
	OwningKey       *secp256k1.PrivateKey
	FundedKeys      []*secp256k1.PrivateKey
	SubnetConfig    string
	BlockchainSpecs []BlockchainSpec
	NodeSpecs       []NodeSpec
}

// Specifies the configuration for a new blockchain.
type BlockchainSpec struct {
	VMName      string
	ChainConfig string
	Genesis     []byte
}

// Specifies the configuration for one or more nodes.
type NodeSpec struct {
	Flags FlagsMap
	Count int
}

// Collects the result of subnet creation
type Subnet struct {
	Spec          SubnetSpec
	ID            ids.ID
	BlockchainIDs []ids.ID
	nodes         []Node
	NodeIDs       []ids.NodeID
}

func (s *Subnet) GetNodes(network Network) ([]Node, error) {
	if s.nodes == nil {
		nodes, err := network.GetEphemeralNodes(s.NodeIDs)
		if err != nil {
			return nil, err
		}
		s.nodes = nodes
	}
	return s.nodes, nil
}

func (s *Subnet) GetNodeURIs(network Network) ([]NodeURI, error) {
	nodes, err := s.GetNodes(network)
	if err != nil {
		return nil, err
	}
	return GetNodeURIs(nodes), nil
}

func GetVMID(vmName string) (ids.ID, error) {
	if len(vmName) > 32 {
		return ids.Empty, fmt.Errorf("VM name must be <= 32 bytes, found %d", len(vmName))
	}
	b := make([]byte, 32)
	copy(b, []byte(vmName))
	return ids.ToID(b)
}

type NodeCleanupFunc func(Node)

func CreateSubnets(
	w io.Writer,
	txTimeout time.Duration,
	pWallet p.Wallet,
	network Network,
	nodeCleanupFunc NodeCleanupFunc,
	subnetSpecs ...SubnetSpec,
) (
	[]*Subnet,
	error,
) {
	subnets := make([]*Subnet, len(subnetSpecs))
	for i, subnetSpec := range subnetSpecs {
		subnet, err := CreateSubnet(
			w,
			context.Background(),
			txTimeout,
			pWallet,
			network,
			nodeCleanupFunc,
			subnetSpec,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create subnet %d: %w", i, err)
		}
		subnets[i] = subnet
	}

	allNodes := []Node{}
	for _, subnet := range subnets {
		allNodes = append(allNodes, subnet.nodes...)
	}

	if _, err := fmt.Fprintf(w, "waiting for new nodes to report healthy\n"); err != nil {
		return nil, err
	}
	// Wait to check health until after nodes have been started and added as validators to
	// minimize the duration required for both nodes to report healthy.
	for _, node := range allNodes {
		ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
		defer cancel()
		err := WaitForHealthy(ctx, node)
		if err != nil {
			return nil, err
		}
		if _, err := fmt.Fprintf(w, " %s is healthy @ %s\n", node.GetID(), node.GetProcessContext().URI); err != nil {
			return nil, err
		}
	}

	// Wait for new nodes to become active validators

	if _, err := fmt.Fprintf(w, "waiting for new nodes to become active validators of the primary network\n"); err != nil {
		return nil, err
	}
	nodeIDs := make([]ids.NodeID, len(allNodes))
	for i := range allNodes {
		nodeIDs[i] = allNodes[i].GetID()
	}
	nodeURI := allNodes[0].GetProcessContext().URI
	if err := waitForActiveValidators(w, nodeURI, constants.PrimaryNetworkID, "the primary network", nodeIDs); err != nil {
		return nil, err
	}

	for _, subnet := range subnets {
		nodeIDs := make([]ids.NodeID, len(subnet.nodes))
		for i, node := range subnet.nodes {
			nodeIDs[i] = node.GetID()
		}
		description := fmt.Sprintf("subnet %s", subnet.ID)
		if _, err := fmt.Fprintf(w, "waiting for new nodes to become active validators of %s\n", description); err != nil {
			return nil, err
		}
		if err := waitForActiveValidators(w, nodeURI, subnet.ID, description, nodeIDs); err != nil {
			return nil, err
		}
	}

	err := network.WriteSubnets(subnets)
	if err != nil {
		return nil, err
	}

	return subnets, nil
}

func CreateSubnet(
	w io.Writer,
	rootContext context.Context,
	txTimeout time.Duration,
	pWallet p.Wallet,
	network Network,
	nodeCleanupFunc NodeCleanupFunc,
	spec SubnetSpec,
) (
	*Subnet,
	error,
) {
	owner := &secp256k1fx.OutputOwners{
		Threshold: 1,
		Addrs: []ids.ShortID{
			spec.OwningKey.Address(),
		},
	}

	if _, err := fmt.Fprintf(w, "creating a new subnet\n"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(rootContext, txTimeout)
	defer cancel()
	subnetTx, err := pWallet.IssueCreateSubnetTx(
		owner,
		common.WithContext(ctx),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create subnet: %w", err)
	}
	subnetID := subnetTx.ID()

	blockchainIDs := make([]ids.ID, len(spec.BlockchainSpecs))
	for i, blockchainSpec := range spec.BlockchainSpecs {
		if _, err := fmt.Fprintf(w, "creating a new blockchain on subnet %s\n", subnetID); err != nil {
			return nil, err
		}
		vmID, err := GetVMID(blockchainSpec.VMName)
		if err != nil {
			return nil, fmt.Errorf("failed to derive VM ID from its name: %w", err)
		}
		ctx, cancel := context.WithTimeout(rootContext, txTimeout)
		defer cancel()
		createChainTx, err := pWallet.IssueCreateChainTx(
			subnetID,
			blockchainSpec.Genesis,
			vmID,
			nil,
			blockchainSpec.VMName,
			common.WithContext(ctx),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create blockchain: %w", err)
		}
		blockchainIDs[i] = createChainTx.ID()
	}

	if _, err := fmt.Fprintf(w, "creating nodes for subnet %s\n", subnetID); err != nil {
		return nil, err
	}
	allNodes := []Node{}
	for _, nodeSpec := range spec.NodeSpecs {
		// Copy before modifying
		flags := nodeSpec.Flags.Copy()
		flags[config.TrackSubnetsKey] = subnetID.String()
		for i := 0; i < nodeSpec.Count; i++ {
			node, err := network.AddEphemeralNode(ctx, w, flags)
			if err != nil {
				return nil, err
			}
			if nodeCleanupFunc != nil {
				nodeCleanupFunc(node)
			}
			allNodes = append(allNodes, node)
		}
	}

	allNodeIDs := make([]ids.NodeID, len(allNodes))
	for i, node := range allNodes {
		allNodeIDs[i] = node.GetID()
	}

	// Add new nodes as validators of the primary network
	delegationPercent := 0.10 // 10%
	delegationFee := uint32(reward.PercentDenominator * delegationPercent)

	for _, node := range allNodes {
		nodeID := node.GetID()

		if _, err := fmt.Fprintf(w, "deriving proof of possession for %s\n", nodeID); err != nil {
			return nil, err
		}
		signingKey, err := node.GetConfig().Flags.GetStringVal(config.StakingSignerKeyContentKey)
		if err != nil {
			return nil, err
		}
		signingKeyBytes, err := base64.StdEncoding.DecodeString(signingKey)
		if err != nil {
			return nil, err
		}
		secretKey, err := bls.SecretKeyFromBytes(signingKeyBytes)
		if err != nil {
			return nil, err
		}
		proofOfPossession := signer.NewProofOfPossession(secretKey)

		// The end time will be reused as the end time for subnet validation
		now := time.Now()
		endTime := uint64(now.Add(genesis.LocalParams.MaxStakeDuration).Unix())

		if _, err := fmt.Fprintf(w, "adding %s as a validator of the primary network\n", nodeID); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(rootContext, txTimeout)
		defer cancel()
		_, err = pWallet.IssueAddPermissionlessValidatorTx(
			&txs.SubnetValidator{
				Validator: txs.Validator{
					NodeID: nodeID,
					Start:  uint64(now.Add(DefaultValidatorStartTimeDiff).Unix()),
					End:    endTime,
					Wght:   genesis.LocalParams.MinValidatorStake,
				},
				Subnet: ids.Empty,
			},
			proofOfPossession,
			pWallet.AVAXAssetID(),
			owner, // validation owner
			owner, // delegation owner
			delegationFee,
			common.WithContext(ctx),
		)
		if err != nil {
			return nil, err
		}

		if _, err := fmt.Fprintf(w, "adding %s as a validator of subnet %s\n", nodeID, subnetID); err != nil {
			return nil, err
		}
		ctx, cancel = context.WithTimeout(rootContext, txTimeout)
		defer cancel()
		_, err = pWallet.IssueAddSubnetValidatorTx(
			&txs.SubnetValidator{
				Validator: txs.Validator{
					NodeID: nodeID,
					Start:  uint64(time.Now().Add(DefaultValidatorStartTimeDiff).Unix()),
					End:    endTime,
					Wght:   units.Schmeckle,
				},
				Subnet: subnetID,
			},
			common.WithContext(ctx),
		)
		if err != nil {
			return nil, err
		}
	}

	return &Subnet{
		Spec:          spec,
		ID:            subnetID,
		BlockchainIDs: blockchainIDs,
		nodes:         allNodes,
		NodeIDs:       allNodeIDs,
	}, nil
}

func waitForActiveValidators(w io.Writer, uri string, subnetID ids.ID, subnetDescription string, nodeIDs []ids.NodeID) error {
	pChainClient := platformvm.NewClient(uri)

	ticker := time.NewTicker(DefaultPollingInterval)
	defer ticker.Stop()

	if _, err := fmt.Fprintf(w, " "); err != nil {
		return err
	}

	rootContext, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	for {
		if _, err := fmt.Fprintf(w, "."); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(rootContext, DefaultTimeout)
		defer cancel()
		validators, err := pChainClient.GetCurrentValidators(rootContext, subnetID, nil)
		if err != nil {
			return err
		}
		validatorSet := set.NewSet[ids.NodeID](len(validators))
		for _, validator := range validators {
			validatorSet.Add(validator.NodeID)
		}
		allActive := true
		for _, nodeID := range nodeIDs {
			if !validatorSet.Contains(nodeID) {
				allActive = false
			}
		}
		if allActive {
			if _, err := fmt.Fprintf(w, "\n saw the expected active validators of %s\n", subnetDescription); err != nil {
				return err
			}
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("failed to see the expected active validators of %s before timeout", subnetDescription)
		case <-ticker.C:
		}
	}
}
