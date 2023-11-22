// Copyright (C) 2019-2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package tmpnet

import (
	"context"
	"io"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/node"
)

// Defines network capabilities supportable regardless of how a network is orchestrated.
type Network interface {
	GetConfig() NetworkConfig
	GetNodes() []Node
	AddEphemeralNode(ctx context.Context, w io.Writer, flags FlagsMap) (Node, error)
	GetEphemeralNodes(nodeIDs []ids.NodeID) ([]Node, error)
	GetSubnets() ([]*Subnet, error)
	WriteSubnets([]*Subnet) error
	RestartSubnets(ctx context.Context, w io.Writer, subnets ...*Subnet) error
}

// Defines node capabilities supportable regardless of how a network is orchestrated.
type Node interface {
	GetID() ids.NodeID
	GetConfig() NodeConfig
	GetProcessContext() node.NodeProcessContext
	IsHealthy(ctx context.Context) (bool, error)
	Stop(ctx context.Context, waitForStopped bool) error
	WaitForProcessStopped(ctx context.Context) error
	Restart(ctx context.Context, w io.Writer, defaultExecPath string, bootstrapIPs []string, bootstrapIDs []string) error
}
