// Copyright (C) 2019-2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package p

import (
	"fmt"
	"math"
	"os"
	"path/filepath"

	ginkgo "github.com/onsi/ginkgo/v2"

	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/config"
	"github.com/ava-labs/avalanchego/tests"
	"github.com/ava-labs/avalanchego/tests/e2e"
	"github.com/ava-labs/avalanchego/tests/fixture/tmpnet"
	"github.com/ava-labs/avalanchego/utils/crypto/secp256k1"
	"github.com/ava-labs/avalanchego/utils/units"
	"github.com/ava-labs/avalanchego/vms/example/xsvm/cmd/issue/export"
	"github.com/ava-labs/avalanchego/vms/example/xsvm/cmd/issue/importtx"
	"github.com/ava-labs/avalanchego/vms/example/xsvm/cmd/issue/transfer"
	"github.com/ava-labs/avalanchego/vms/example/xsvm/genesis"
)

var _ = e2e.DescribePChain("[Warp]", func() {
	require := require.New(ginkgo.GinkgoT())

	const xsvmPluginFilename = "v3m4wPxaHpvGr8qfMeyK6PRW3idZrPHmYcMTt7oXdK47yurVH"

	ginkgo.It("should support transfers between subnets", func() {
		subnetAName := "e2e-warp-a"
		subnetBName := "e2e-warp-b"

		ginkgo.By("checking if the subnets already exist")
		network := e2e.Env.GetNetwork()
		subnets, err := network.GetSubnets()
		require.NoError(err)
		var (
			sourceSubnet      *tmpnet.Subnet
			destinationSubnet *tmpnet.Subnet
		)
		for _, subnet := range subnets {
			if subnet.Spec.Name == subnetAName {
				sourceSubnet = subnet
			}
			if subnet.Spec.Name == subnetBName {
				destinationSubnet = subnet
			}
		}
		keyFactory := secp256k1.Factory{}
		if sourceSubnet != nil && destinationSubnet != nil {
			tests.Outf(" subnets exist and will be reused\n")
			if len(os.Getenv("E2E_RESTART_SUBNETS")) > 0 {
				ginkgo.By("restarting subnets")
				require.NoError(network.RestartSubnets(e2e.DefaultContext(), ginkgo.GinkgoWriter, subnets...))
			}
		} else {
			tests.Outf(" subnets do not yet exist and will be created\n")

			xsvmPluginPath := filepath.Join(e2e.Env.PluginDir, xsvmPluginFilename)
			ginkgo.By(fmt.Sprintf("checking that xsvm plugin binary exists at path %s", xsvmPluginPath), func() {
				_, err := os.Stat(xsvmPluginPath)
				require.NoError(err)
			})

			ginkgo.By("creating a wallet with a funded key that will create the subnets")
			nodeURI := e2e.Env.GetRandomNodeURI()
			keychain := e2e.Env.NewKeychain(1)
			privateKey := keychain.Keys[0]
			baseWallet := e2e.Env.NewWallet(keychain, nodeURI)
			pWallet := baseWallet.P()

			ginkgo.By("defining the specification of an xsvm subnet")
			fundedKey, err := keyFactory.NewPrivateKey()
			require.NoError(err)
			genesisBytes, err := genesis.Codec.Marshal(genesis.Version, &genesis.Genesis{
				Timestamp: 0,
				Allocations: []genesis.Allocation{
					{
						Address: fundedKey.Address(),
						Balance: math.MaxUint64,
					},
				},
			})
			require.NoError(err)
			subnetSpec := tmpnet.SubnetSpec{
				OwningKey: privateKey,
				FundedKeys: []*secp256k1.PrivateKey{
					fundedKey,
				},
				BlockchainSpecs: []tmpnet.BlockchainSpec{
					{
						VMName:  "xsvm",
						Genesis: genesisBytes,
					},
				},
				NodeSpecs: []tmpnet.NodeSpec{
					{
						Flags: tmpnet.FlagsMap{
							config.PluginDirKey: e2e.Env.PluginDir,
						},
						Count: 1,
					},
				},
			}
			subnetASpec := subnetSpec
			subnetASpec.Name = subnetAName
			subnetBSpec := subnetSpec
			subnetBSpec.Name = subnetBName
			ginkgo.By("creating 2 xsvm subnets")
			nodeCleanupFunc := e2e.RegisterNodeforCleanup
			// TODO(marun) if not retaining a subnet, maybe use a private network to ensure cleanup?
			if e2e.Env.RetainSubnets {
				tests.Outf(" subnet nodes will be left running\n")
				nodeCleanupFunc = nil
			}
			subnets, err := tmpnet.CreateSubnets(
				ginkgo.GinkgoWriter,
				e2e.DefaultTimeout,
				pWallet,
				network,
				nodeCleanupFunc,
				subnetASpec,
				subnetBSpec,
			)
			require.NoError(err)

			sourceSubnet = subnets[0]
			destinationSubnet = subnets[1]
		}

		sourceNodeURIs, err := sourceSubnet.GetNodeURIs(network)
		require.NoError(err)
		sourceChainID := sourceSubnet.BlockchainIDs[0]
		sourcePrivateKey := sourceSubnet.Spec.FundedKeys[0]

		destinationNodeURIs, err := destinationSubnet.GetNodeURIs(network)
		require.NoError(err)
		destinationChainID := destinationSubnet.BlockchainIDs[0]
		destinationPrivateKey := destinationSubnet.Spec.FundedKeys[0]

		ginkgo.By(fmt.Sprintf("exporting from blockchain %s on subnet %s", sourceChainID, sourceSubnet.ID))
		exportNodeURI := sourceNodeURIs[0]
		tests.Outf(" issuing transactions on %s (%s)\n", exportNodeURI.NodeID, exportNodeURI.URI)
		exportTxStatus, err := export.Export(
			e2e.DefaultContext(),
			&export.Config{
				URI:                exportNodeURI.URI,
				SourceChainID:      sourceChainID,
				DestinationChainID: destinationChainID,
				Amount:             units.Schmeckle,
				To:                 destinationPrivateKey.Address(),
				PrivateKey:         sourcePrivateKey,
			},
		)
		require.NoError(err)
		tests.Outf(" issued transaction with ID: %s\n", exportTxStatus.TxID)

		ginkgo.By(fmt.Sprintf("issuing transactions on chain %s on subnet %s to activate snowman++ consensus",
			destinationChainID, destinationSubnet.ID))
		destinationNodeURI := destinationNodeURIs[0]
		tests.Outf(" issuing transactions on %s (%s)\n", destinationNodeURI.NodeID, destinationNodeURI.URI)
		recipientKey, err := keyFactory.NewPrivateKey()
		require.NoError(err)
		for i := 0; i < 3; i++ {
			transferTxStatus, err := transfer.Transfer(
				e2e.DefaultContext(),
				&transfer.Config{
					URI:        destinationNodeURI.URI,
					ChainID:    destinationChainID,
					AssetID:    destinationChainID,
					Amount:     units.Schmeckle,
					To:         recipientKey.Address(),
					PrivateKey: destinationPrivateKey,
				},
			)
			require.NoError(err)
			tests.Outf(" issued transaction with ID: %s\n", transferTxStatus.TxID)
		}

		ginkgo.By(fmt.Sprintf("importing to blockchain %s on subnet %s", destinationChainID, destinationSubnet.ID))
		tests.Outf(" issuing transactions on %s (%s)\n", destinationNodeURI.NodeID, destinationNodeURI.URI)
		sourceURIs := make([]string, len(sourceNodeURIs))
		for i, nodeURI := range sourceNodeURIs {
			sourceURIs[i] = nodeURI.URI
		}
		importTxStatus, err := importtx.Import(
			e2e.DefaultContext(),
			&importtx.Config{
				URI:                destinationNodeURI.URI,
				SourceURIs:         sourceURIs,
				SourceChainID:      sourceChainID.String(),
				DestinationChainID: destinationChainID.String(),
				TxID:               exportTxStatus.TxID,
				PrivateKey:         destinationPrivateKey,
			},
		)
		require.NoError(err)
		tests.Outf(" issued transaction with ID: %s\n", importTxStatus.TxID)
		tests.Outf(" waiting for transaction to be accepted...\n")

		// TODO(marun) Verify the balances on both chains
		// TODO(marun) Ensure a short validation period if persistence is not configured? or use a private network?
	})
})
