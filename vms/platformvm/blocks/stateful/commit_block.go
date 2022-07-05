// Copyright (C) 2019-2021, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package stateful

import (
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow/choices"
	"github.com/ava-labs/avalanchego/vms/platformvm/blocks/stateless"
)

var (
	_ Block    = &CommitBlock{}
	_ Decision = &CommitBlock{}
)

// CommitBlock being accepted results in the proposal of its parent (which must
// be a proposal block) being enacted.
type CommitBlock struct {
	Manager
	*stateless.CommitBlock
	*decisionBlock

	wasPreferred bool
}

// NewCommitBlock returns a new *Commit block where the block's parent, a
// proposal block, has ID [parentID]. Additionally the block will track if it
// was originally preferred or not for metrics.
func NewCommitBlock(
	manager Manager,
	parentID ids.ID,
	height uint64,
	wasPreferred bool,
) (*CommitBlock, error) {
	statelessBlk, err := stateless.NewCommitBlock(parentID, height)
	if err != nil {
		return nil, err
	}

	return toStatefulCommitBlock(statelessBlk, manager, wasPreferred, choices.Processing)
}

func toStatefulCommitBlock(
	statelessBlk *stateless.CommitBlock,
	manager Manager,
	wasPreferred bool,
	status choices.Status,
) (*CommitBlock, error) {
	commit := &CommitBlock{
		CommitBlock: statelessBlk,
		Manager:     manager,
		decisionBlock: &decisionBlock{
			chainState: manager,
			commonBlock: &commonBlock{
				timestampGetter: manager,
				lastAccepteder:  manager,
				baseBlk:         &statelessBlk.CommonBlock,
				status:          status,
			},
		},
		wasPreferred: wasPreferred,
	}

	return commit, nil
}

// Verify this block performs a valid state transition.
//
// The parent block must be a proposal
//
// This function also sets onAcceptState if the verification passes.
func (c *CommitBlock) Verify() error {
	return c.verifyCommitBlock(c)
}

func (c *CommitBlock) Accept() error {
	return c.acceptCommitBlock(c)
}

func (c *CommitBlock) Reject() error {
	return c.rejectCommitBlock(c)
}

func (c *CommitBlock) conflicts(s ids.Set) (bool, error) {
	return c.conflictsCommitBlock(c, s)
}

func (c *CommitBlock) free() {
	c.freeCommitBlock(c)
}

func (c *CommitBlock) setBaseState() {
	c.setBaseStateCommitBlock(c)
}
