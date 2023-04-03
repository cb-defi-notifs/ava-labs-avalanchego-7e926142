// Copyright (C) 2019-2022, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package validators

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ava-labs/avalanchego/cache"
	"github.com/ava-labs/avalanchego/database"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow/validators"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/math"
	"github.com/ava-labs/avalanchego/utils/timer/mockable"
	"github.com/ava-labs/avalanchego/utils/window"
	"github.com/ava-labs/avalanchego/vms/platformvm/config"
	"github.com/ava-labs/avalanchego/vms/platformvm/metrics"
	"github.com/ava-labs/avalanchego/vms/platformvm/state"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
)

const (
	validatorSetsCacheSize        = 64
	maxRecentlyAcceptedWindowSize = 256
	recentlyAcceptedWindowTTL     = 5 * time.Minute
)

var (
	_ validators.State = (*set)(nil)

	ErrMissingValidatorSet = errors.New("missing validator set")
)

// P-chain must be able to provide information about validators active
// at different heights. [QueribleSet] interface encapsulates all the machinery
// to achieve this.
type QueribleSet interface {
	validators.State

	GetValidatorIDs(subnetID ids.ID) ([]ids.NodeID, bool)
}

// Set interface adds to QueribleSet the ability to blocks IDs
// to serve GetMinimumHeight
type Set interface {
	QueribleSet
	Track(blkID ids.ID)
}

func NewSet(
	cfg config.Config,
	state state.State,
	metrics metrics.Metrics,
	clk mockable.Clock,
) Set {
	return &set{
		cfg:     cfg,
		state:   state,
		metrics: metrics,
		clk:     &clk,
		caches:  make(map[ids.ID]cache.Cacher[uint64, map[ids.NodeID]*validators.GetValidatorOutput]),
		recentlyAccepted: window.New[ids.ID](
			window.Config{
				Clock:   &clk,
				MaxSize: maxRecentlyAcceptedWindowSize,
				TTL:     recentlyAcceptedWindowTTL,
			},
		),
	}
}

type set struct {
	cfg     config.Config
	state   state.State
	metrics metrics.Metrics
	clk     *mockable.Clock

	// cachesMux protects addition of a new subnet cache to caches
	// so that [GetValidatorSet] can be carried out with RLock only
	cachesMux sync.Mutex

	// [caches] Maps caches for each subnet that is currently tracked.
	// Key: Subnet ID
	// Value: cache mapping height -> validator set map
	caches map[ids.ID]cache.Cacher[uint64, map[ids.NodeID]*validators.GetValidatorOutput]

	// sliding window of blocks that were recently accepted
	recentlyAccepted window.Window[ids.ID]
}

// GetMinimumHeight returns the height of the most recent block beyond the
// horizon of our recentlyAccepted window.
//
// Because the time between blocks is arbitrary, we're only guaranteed that
// the window's configured TTL amount of time has passed once an element
// expires from the window.
//
// To try to always return a block older than the window's TTL, we return the
// parent of the oldest element in the window (as an expired element is always
// guaranteed to be sufficiently stale). If we haven't expired an element yet
// in the case of a process restart, we default to the lastAccepted block's
// height which is likely (but not guaranteed) to also be older than the
// window's configured TTL.
//
// If [UseCurrentHeight] is true, we will always return the last accepted block
// height as the minimum. This is used to trigger the proposervm on recently
// created subnets before [recentlyAcceptedWindowTTL].
//
// GetMinimumHeight assumes ctx.RLock() is hold
func (vs *set) GetMinimumHeight(ctx context.Context) (uint64, error) {
	if vs.cfg.UseCurrentHeight {
		return vs.GetCurrentHeight(ctx)
	}

	oldest, ok := vs.recentlyAccepted.Oldest()
	if !ok {
		return vs.GetCurrentHeight(ctx)
	}

	blk, _, err := vs.state.GetStatelessBlock(oldest)
	if err != nil {
		return 0, err
	}

	// We subtract 1 from the height of [oldest] because we want the height of
	// the last block accepted before the [recentlyAccepted] window.
	//
	// There is guaranteed to be a block accepted before this window because the
	// first block added to [recentlyAccepted] window is >= height 1.
	return blk.Height() - 1, nil
}

// GetCurrentHeight returns the height of the last accepted block
// GetCurrentHeight assumes ctx.RLock() is hold
func (vs *set) GetCurrentHeight(context.Context) (uint64, error) {
	lastAcceptedID := vs.state.GetLastAccepted()
	lastAccepted, _, err := vs.state.GetStatelessBlock(lastAcceptedID)
	if err != nil {
		return 0, err
	}
	return lastAccepted.Height(), nil
}

// GetValidatorSet returns the validator set at the specified height for the
// provided subnetID.
//
// GetValidatorSet assumes ctx.RLock() is hold
func (vs *set) GetValidatorSet(ctx context.Context, height uint64, subnetID ids.ID) (map[ids.NodeID]*validators.GetValidatorOutput, error) {
	validatorSetsCache := vs.getCache(subnetID)

	if validatorSet, ok := validatorSetsCache.Get(height); ok {
		vs.metrics.IncValidatorSetsCached()
		return validatorSet, nil
	}

	lastAcceptedHeight, err := vs.GetCurrentHeight(ctx)
	if err != nil {
		return nil, err
	}
	if lastAcceptedHeight < height {
		return nil, database.ErrNotFound
	}

	// get the start time to track metrics
	startTime := vs.clk.Time()

	currentSubnetValidators, ok := vs.cfg.Validators.Get(subnetID)
	if !ok {
		currentSubnetValidators = validators.NewSet()
		if err := vs.state.ValidatorSet(subnetID, currentSubnetValidators); err != nil {
			return nil, err
		}
	}
	currentPrimaryNetworkValidators, ok := vs.cfg.Validators.Get(constants.PrimaryNetworkID)
	if !ok {
		// This should never happen
		return nil, ErrMissingValidatorSet
	}

	currentSubnetValidatorList := currentSubnetValidators.List()
	vdrSet := make(map[ids.NodeID]*validators.GetValidatorOutput, len(currentSubnetValidatorList))
	for _, vdr := range currentSubnetValidatorList {
		primaryVdr, ok := currentPrimaryNetworkValidators.Get(vdr.NodeID)
		if !ok {
			// This should never happen
			return nil, fmt.Errorf("%w: %s", ErrMissingValidatorSet, vdr.NodeID)
		}
		vdrSet[vdr.NodeID] = &validators.GetValidatorOutput{
			NodeID:    vdr.NodeID,
			PublicKey: primaryVdr.PublicKey,
			Weight:    vdr.Weight,
		}
	}

	for i := lastAcceptedHeight; i > height; i-- {
		weightDiffs, err := vs.state.GetValidatorWeightDiffs(i, subnetID)
		if err != nil {
			return nil, err
		}

		for nodeID, weightDiff := range weightDiffs {
			vdr, ok := vdrSet[nodeID]
			if !ok {
				// This node isn't in the current validator set.
				vdr = &validators.GetValidatorOutput{
					NodeID: nodeID,
				}
				vdrSet[nodeID] = vdr
			}

			// The weight of this node changed at this block.
			var op func(uint64, uint64) (uint64, error)
			if weightDiff.Decrease {
				// The validator's weight was decreased at this block, so in the
				// prior block it was higher.
				op = math.Add64
			} else {
				// The validator's weight was increased at this block, so in the
				// prior block it was lower.
				op = math.Sub[uint64]
			}

			// Apply the weight change.
			vdr.Weight, err = op(vdr.Weight, weightDiff.Amount)
			if err != nil {
				return nil, err
			}

			if vdr.Weight == 0 {
				// The validator's weight was 0 before this block so
				// they weren't in the validator set.
				delete(vdrSet, nodeID)
			}
		}

		pkDiffs, err := vs.state.GetValidatorPublicKeyDiffs(i)
		if err != nil {
			return nil, err
		}

		for nodeID, pk := range pkDiffs {
			// pkDiffs includes all primary network key diffs, if we are
			// fetching a subnet's validator set, we should ignore non-subnet
			// validators.
			if vdr, ok := vdrSet[nodeID]; ok {
				// The validator's public key was removed at this block, so it
				// was in the validator set before.
				vdr.PublicKey = pk
			}
		}
	}

	// cache the validator set
	validatorSetsCache.Put(height, vdrSet)

	endTime := vs.clk.Time()
	vs.metrics.IncValidatorSetsCreated()
	vs.metrics.AddValidatorSetsDuration(endTime.Sub(startTime))
	vs.metrics.AddValidatorSetsHeightDiff(lastAcceptedHeight - height)
	return vdrSet, nil
}

// GetCurrentHeight returns the height of the last accepted block
func (vs *set) GetSubnetID(_ context.Context, chainID ids.ID) (ids.ID, error) {
	if chainID == constants.PlatformChainID {
		return constants.PrimaryNetworkID, nil
	}

	chainTx, _, err := vs.state.GetTx(chainID)
	if err != nil {
		return ids.Empty, fmt.Errorf(
			"problem retrieving blockchain %q: %w",
			chainID,
			err,
		)
	}
	chain, ok := chainTx.Unsigned.(*txs.CreateChainTx)
	if !ok {
		return ids.Empty, fmt.Errorf("%q is not a blockchain", chainID)
	}
	return chain.SubnetID, nil
}

func (vs *set) GetValidatorIDs(subnetID ids.ID) ([]ids.NodeID, bool) {
	validatorSet, exist := vs.cfg.Validators.Get(subnetID)
	if !exist {
		return nil, false
	}
	validators := validatorSet.List()

	validatorIDs := make([]ids.NodeID, len(validators))
	for i, vdr := range validators {
		validatorIDs[i] = vdr.NodeID
	}

	return validatorIDs, true
}

func (vs *set) Track(blkID ids.ID) {
	vs.recentlyAccepted.Add(blkID)
}

// getCache returns cache associated with subnetID. It creates it
// if not available. Lock guard ensures validators.State methods
// can be accessed with read lock.
func (vs *set) getCache(subnetID ids.ID) cache.Cacher[uint64, map[ids.NodeID]*validators.GetValidatorOutput] {
	vs.cachesMux.Lock()
	defer vs.cachesMux.Unlock()
	validatorSetsCache, exists := vs.caches[subnetID]
	if !exists {
		validatorSetsCache = &cache.LRU[uint64, map[ids.NodeID]*validators.GetValidatorOutput]{Size: validatorSetsCacheSize}
		// Only cache whitelisted subnets
		if subnetID == constants.PrimaryNetworkID || vs.cfg.TrackedSubnets.Contains(subnetID) {
			vs.caches[subnetID] = validatorSetsCache
		}
	}
	return validatorSetsCache
}
