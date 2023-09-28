// Copyright (C) 2019-2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package genesis

import (
	"time"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/crypto/secp256k1"
	"github.com/ava-labs/avalanchego/utils/hashing"
	"github.com/ava-labs/avalanchego/utils/units"
	"github.com/ava-labs/avalanchego/vms/components/avax"
	"github.com/ava-labs/avalanchego/vms/platformvm/reward"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs/txheap"
	"github.com/ava-labs/avalanchego/vms/secp256k1fx"
)

var (
	TestKeys               = secp256k1.TestKeys()
	TestGenesisTime        = time.Date(1997, 1, 1, 0, 0, 0, 0, time.UTC)
	TestValidateStartTime  = TestGenesisTime
	TestMinStakingDuration = 24 * time.Hour
	TestMaxStakingDuration = 365 * 24 * time.Hour
	TestValidateEndTime    = TestValidateStartTime.Add(10 * TestMinStakingDuration)

	TestAvaxAssetID = ids.ID{'y', 'e', 'e', 't'}
	TestXChainID    = ids.Empty.Prefix(0)
	TestCChainID    = ids.Empty.Prefix(1)

	TestMinValidatorStake = 5 * units.MilliAvax
	TestBalance           = 100 * TestMinValidatorStake
	TestWeight            = 10 * units.KiloAvax
)

func BuildTestGenesis(networkID uint32) (*State, error) {
	genesisUtxos := make([]*avax.UTXO, len(TestKeys))
	for i, key := range TestKeys {
		addr := key.PublicKey().Address()
		genesisUtxos[i] = &avax.UTXO{
			UTXOID: avax.UTXOID{
				TxID:        ids.Empty,
				OutputIndex: uint32(i),
			},
			Asset: avax.Asset{ID: TestAvaxAssetID},
			Out: &secp256k1fx.TransferOutput{
				Amt: TestBalance,
				OutputOwners: secp256k1fx.OutputOwners{
					Locktime:  0,
					Threshold: 1,
					Addrs:     []ids.ShortID{addr},
				},
			},
		}
	}

	vdrs := txheap.NewByEndTime()
	for _, key := range TestKeys {
		addr := key.PublicKey().Address()
		nodeID := ids.NodeID(key.PublicKey().Address())

		utxo := &avax.TransferableOutput{
			Asset: avax.Asset{ID: TestAvaxAssetID},
			Out: &secp256k1fx.TransferOutput{
				Amt: TestWeight,
				OutputOwners: secp256k1fx.OutputOwners{
					Locktime:  0,
					Threshold: 1,
					Addrs:     []ids.ShortID{addr},
				},
			},
		}

		owner := &secp256k1fx.OutputOwners{
			Locktime:  0,
			Threshold: 1,
			Addrs:     []ids.ShortID{addr},
		}

		tx := &txs.Tx{Unsigned: &txs.AddValidatorTx{
			BaseTx: txs.BaseTx{BaseTx: avax.BaseTx{
				NetworkID:    networkID,
				BlockchainID: constants.PlatformChainID,
			}},
			Validator: txs.Validator{
				NodeID: nodeID,
				Start:  uint64(TestValidateStartTime.Unix()),
				End:    uint64(TestValidateEndTime.Unix()),
				Wght:   utxo.Output().Amount(),
			},
			StakeOuts:        []*avax.TransferableOutput{utxo},
			RewardsOwner:     owner,
			DelegationShares: reward.PercentDenominator,
		}}
		if err := tx.Initialize(txs.GenesisCodec); err != nil {
			return nil, err
		}

		vdrs.Add(tx)
	}

	return &State{
		GenesisBlkID:  hashing.ComputeHash256Array(ids.Empty[:]),
		UTXOs:         genesisUtxos,
		Validators:    vdrs.List(),
		Chains:        nil,
		Timestamp:     uint64(TestGenesisTime.Unix()),
		InitialSupply: 360 * units.MegaAvax,
	}, nil
}