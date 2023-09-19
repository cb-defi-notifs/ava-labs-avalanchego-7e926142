// Copyright (C) 2019-2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ava-labs/avalanchego/config"
	"github.com/ava-labs/avalanchego/genesis"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/tests/fixture/tmpnet"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/crypto/secp256k1"
	"github.com/ava-labs/avalanchego/utils/formatting/address"
	"github.com/ava-labs/avalanchego/utils/perms"
	"github.com/ava-labs/avalanchego/utils/set"
	"github.com/ava-labs/avalanchego/vms/platformvm/reward"
)

const (
	// This interval was chosen to avoid spamming node APIs during
	// startup, as smaller intervals (e.g. 50ms) seemed to noticeably
	// increase the time for a network's nodes to be seen as healthy.
	networkHealthCheckInterval = 200 * time.Millisecond

	defaultEphemeralDirName = "ephemeral"

	defaultSubnetDirName = "subnets"

	defaultChainConfigFilename = "config.json"
)

var (
	errInvalidNodeCount      = errors.New("failed to populate local network config: non-zero node count is only valid for a network without nodes")
	errInvalidKeyCount       = errors.New("failed to populate local network config: non-zero key count is only valid for a network without keys")
	errLocalNetworkDirNotSet = errors.New("local network directory not set - has Create() been called?")
	errInvalidNetworkDir     = errors.New("failed to write local network: invalid network directory")
	errMissingBootstrapNodes = errors.New("failed to add node due to missing bootstrap nodes")
)

// Default root dir for storing networks and their configuration.
func GetDefaultRootDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".tmpnet", "networks"), nil
}

// Find the next available network ID by attempting to create a
// directory numbered from 1000 until creation succeeds. Returns the
// network id and the full path of the created directory.
func FindNextNetworkID(rootDir string) (uint32, string, error) {
	var (
		networkID uint32 = 1000
		dirPath   string
	)
	for {
		_, reserved := constants.NetworkIDToNetworkName[networkID]
		if reserved {
			networkID++
			continue
		}

		dirPath = filepath.Join(rootDir, strconv.FormatUint(uint64(networkID), 10))
		err := os.Mkdir(dirPath, perms.ReadWriteExecute)
		if err == nil {
			return networkID, dirPath, nil
		}

		if !errors.Is(err, fs.ErrExist) {
			return 0, "", fmt.Errorf("failed to create network directory: %w", err)
		}

		// Directory already exists, keep iterating
		networkID++
	}
}

// Defines the configuration required for a local network (i.e. one composed of local processes).
type LocalNetwork struct {
	tmpnet.NetworkConfig
	LocalConfig

	// Nodes with local configuration
	Nodes []*LocalNode

	// Path where network configuration will be stored
	Dir string
}

// Returns the configuration of the network in backend-agnostic form.
func (ln *LocalNetwork) GetConfig() tmpnet.NetworkConfig {
	return ln.NetworkConfig
}

// Returns the nodes of the network in backend-agnostic form.
func (ln *LocalNetwork) GetNodes() []tmpnet.Node {
	return localNodeSliceToNodeSlice(ln.Nodes)
}

// Adds a backend-agnostic ephemeral node to the network
func (ln *LocalNetwork) AddEphemeralNode(w io.Writer, flags tmpnet.FlagsMap) (tmpnet.Node, error) {
	if flags == nil {
		flags = tmpnet.FlagsMap{}
	} else {
		// Avoid modifying the input flags map
		flags = flags.Copy()
	}
	return ln.AddLocalNode(w, &LocalNode{
		NodeConfig: tmpnet.NodeConfig{
			Flags: flags,
		},
	}, true /* isEphemeral */)
}

// Starts a new network stored under the provided root dir. Required
// configuration will be defaulted if not provided.
func StartNetwork(
	ctx context.Context,
	w io.Writer,
	rootDir string,
	network *LocalNetwork,
	nodeCount int,
	keyCount int,
) (*LocalNetwork, error) {
	if _, err := fmt.Fprintf(w, "Preparing configuration for new local network with %s\n", network.ExecPath); err != nil {
		return nil, err
	}

	// TODO(marun) Output the version information for avalanchego path

	if len(rootDir) == 0 {
		// Use the default root dir
		var err error
		rootDir, err = GetDefaultRootDir()
		if err != nil {
			return nil, err
		}
	}

	// Ensure creation of the root dir
	if err := os.MkdirAll(rootDir, perms.ReadWriteExecute); err != nil {
		return nil, fmt.Errorf("failed to create root network dir: %w", err)
	}

	// Determine the network path and ID
	var (
		networkDir string
		networkID  uint32
	)
	if network.Genesis != nil && network.Genesis.NetworkID > 0 {
		// Use the network ID defined in the provided genesis
		networkID = network.Genesis.NetworkID
	}
	if networkID > 0 {
		// Use a directory with a random suffix
		var err error
		networkDir, err = os.MkdirTemp(rootDir, fmt.Sprintf("%d.", network.Genesis.NetworkID))
		if err != nil {
			return nil, fmt.Errorf("failed to create network dir: %w", err)
		}
	} else {
		// Find the next available network ID based on the contents of the root dir
		var err error
		networkID, networkDir, err = FindNextNetworkID(rootDir)
		if err != nil {
			return nil, err
		}
	}

	// Setting the network dir before populating config ensures the
	// nodes know where to write their configuration.
	network.Dir = networkDir

	if err := network.PopulateLocalNetworkConfig(networkID, nodeCount, keyCount); err != nil {
		return nil, err
	}

	if err := network.WriteAll(); err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(w, "Starting network %d @ %s\n", network.Genesis.NetworkID, network.Dir); err != nil {
		return nil, err
	}
	if err := network.Start(w); err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(w, "Waiting for all nodes to report healthy...\n\n"); err != nil {
		return nil, err
	}
	if err := network.WaitForHealthy(ctx, w); err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(w, "\nStarted network %d @ %s\n", network.Genesis.NetworkID, network.Dir); err != nil {
		return nil, err
	}
	return network, nil
}

// Read a network from the provided directory.
func ReadNetwork(dir string) (*LocalNetwork, error) {
	// Ensure a real and absolute network dir so that node
	// configuration that embeds the network path will continue to
	// work regardless of symlink and working directory changes.
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	realDir, err := filepath.EvalSymlinks(absDir)
	if err != nil {
		return nil, err
	}

	network := &LocalNetwork{Dir: realDir}
	if err := network.ReadAll(); err != nil {
		return nil, fmt.Errorf("failed to read local network: %w", err)
	}
	return network, nil
}

// Stop the nodes of the network configured in the provided directory.
func StopNetwork(dir string) error {
	network, err := ReadNetwork(dir)
	if err != nil {
		return err
	}
	return network.Stop()
}

// Ensure the network has the configuration it needs to start.
func (ln *LocalNetwork) PopulateLocalNetworkConfig(networkID uint32, nodeCount int, keyCount int) error {
	if len(ln.Nodes) > 0 && nodeCount > 0 {
		return errInvalidNodeCount
	}
	if len(ln.FundedKeys) > 0 && keyCount > 0 {
		return errInvalidKeyCount
	}

	if nodeCount > 0 {
		// Add the specified number of nodes
		nodes := make([]*LocalNode, 0, nodeCount)
		for i := 0; i < nodeCount; i++ {
			nodes = append(nodes, NewLocalNode(""))
		}
		ln.Nodes = nodes
	}

	// Ensure each node has keys and an associated node ID. This
	// ensures the availability of node IDs and proofs of possession
	// for genesis generation.
	for _, node := range ln.Nodes {
		if err := node.EnsureKeys(); err != nil {
			return err
		}
	}

	// Assume all the initial nodes are stakers
	initialStakers, err := stakersForNodes(networkID, ln.Nodes)
	if err != nil {
		return err
	}

	if keyCount > 0 {
		// Ensure there are keys for genesis generation to fund
		keys := make([]*secp256k1.PrivateKey, 0, keyCount)
		for i := 0; i < keyCount; i++ {
			key, err := secp256k1.NewPrivateKey()
			if err != nil {
				return fmt.Errorf("failed to generate private key: %w", err)
			}
			keys = append(keys, key)
		}
		ln.FundedKeys = keys
	}

	if err := ln.EnsureGenesis(networkID, initialStakers); err != nil {
		return err
	}

	if _, ok := ln.ChainConfigs["C"]; !ok {
		if ln.ChainConfigs == nil {
			ln.ChainConfigs = map[string]tmpnet.FlagsMap{}
		}
		ln.ChainConfigs["C"] = LocalCChainConfig()
	}

	// Default flags need to be set in advance of node config
	// population to ensure correct node configuration.
	if ln.DefaultFlags == nil {
		ln.DefaultFlags = LocalFlags()
	}

	for _, node := range ln.Nodes {
		// Ensure the node is configured for use with the network and
		// knows where to write its configuration.
		if err := ln.PopulateNodeConfig(node, ln.Dir); err != nil {
			return err
		}
	}

	return nil
}

// Ensure the provided node has the configuration it needs to start. If the data dir is
// not set, it will be defaulted to [nodeParentDir]/[node ID]. Requires that the
// network has valid genesis data.
func (ln *LocalNetwork) PopulateNodeConfig(node *LocalNode, nodeParentDir string) error {
	flags := node.Flags

	// Set values common to all nodes
	flags.SetDefaults(ln.DefaultFlags)
	flags.SetDefaults(tmpnet.FlagsMap{
		config.GenesisFileKey:    ln.GetGenesisPath(),
		config.ChainConfigDirKey: ln.GetChainConfigDir(),
	})

	// Convert the network id to a string to ensure consistency in JSON round-tripping.
	flags[config.NetworkNameKey] = strconv.FormatUint(uint64(ln.Genesis.NetworkID), 10)

	// Ensure keys are added if necessary
	if err := node.EnsureKeys(); err != nil {
		return err
	}

	// Ensure the node's data dir is configured
	dataDir := node.GetDataDir()
	if len(dataDir) == 0 {
		// NodeID will have been set by EnsureKeys
		dataDir = filepath.Join(nodeParentDir, node.NodeID.String())
		flags[config.DataDirKey] = dataDir
	}

	return nil
}

// Starts a network for the first time
func (ln *LocalNetwork) Start(w io.Writer) error {
	if len(ln.Dir) == 0 {
		return errLocalNetworkDirNotSet
	}

	// Ensure configuration on disk is current
	if err := ln.WriteAll(); err != nil {
		return err
	}

	// Accumulate bootstrap nodes such that each subsequently started
	// node bootstraps from the nodes previously started.
	//
	// e.g.
	// 1st node: no bootstrap nodes
	// 2nd node: 1st node
	// 3rd node: 1st and 2nd nodes
	// ...
	//
	bootstrapIDs := make([]string, 0, len(ln.Nodes))
	bootstrapIPs := make([]string, 0, len(ln.Nodes))

	// Configure networking and start each node
	for _, node := range ln.Nodes {
		// Update network configuration
		node.SetNetworkingConfigDefaults(0, 0, bootstrapIDs, bootstrapIPs)

		// Write configuration to disk in preparation for node start
		if err := node.WriteConfig(); err != nil {
			return err
		}

		// Start waits for the process context to be written which
		// indicates that the node will be accepting connections on
		// its staking port. The network will start faster with this
		// synchronization due to the avoidance of exponential backoff
		// if a node tries to connect to a beacon that is not ready.
		if err := node.Start(w, ln.ExecPath); err != nil {
			return err
		}

		// Collect bootstrap nodes for subsequently started nodes to use
		bootstrapIDs = append(bootstrapIDs, node.NodeID.String())
		bootstrapIPs = append(bootstrapIPs, node.StakingAddress)
	}

	return nil
}

// Wait until all nodes in the network are healthy.
func (ln *LocalNetwork) WaitForHealthy(ctx context.Context, w io.Writer) error {
	ticker := time.NewTicker(networkHealthCheckInterval)
	defer ticker.Stop()

	healthyNodes := set.NewSet[ids.NodeID](len(ln.Nodes))
	for healthyNodes.Len() < len(ln.Nodes) {
		for _, node := range ln.Nodes {
			if healthyNodes.Contains(node.NodeID) {
				continue
			}

			healthy, err := node.IsHealthy(ctx)
			if err != nil && !errors.Is(err, tmpnet.ErrNotRunning) {
				return err
			}
			if !healthy {
				continue
			}

			healthyNodes.Add(node.NodeID)
			if _, err := fmt.Fprintf(w, "%s is healthy @ %s\n", node.NodeID, node.URI); err != nil {
				return err
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("failed to see all nodes healthy before timeout: %w", ctx.Err())
		case <-ticker.C:
		}
	}
	return nil
}

// Retrieve API URIs for all running primary validator nodes. URIs for
// ephemeral nodes are not returned.
func (ln *LocalNetwork) GetURIs() []tmpnet.NodeURI {
	// Cast from []*LocalNode to []tmpnet.Node
	nodes := make([]tmpnet.Node, len(ln.Nodes))
	for i, node := range ln.Nodes {
		nodes[i] = node
	}
	return tmpnet.GetNodeURIs(nodes)
}

// Stop all nodes in the network.
func (ln *LocalNetwork) Stop() error {
	var errs []error
	// Assume the nodes are loaded and the pids are current
	for _, node := range ln.Nodes {
		ctx, cancel := context.WithTimeout(context.Background(), DefaultNodeStopTimeout)
		defer cancel()
		if err := node.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop node %s: %w", node.NodeID, err))
		}
	}
	ephemeralNodes, err := ln.GetEphemeralNodes(nil)
	if err != nil {
		return err
	}
	for _, node := range ephemeralNodes {
		ctx, cancel := context.WithTimeout(context.Background(), DefaultNodeStopTimeout)
		defer cancel()
		if err := node.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop node %s: %w", node.GetID(), err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to stop network:\n%w", errors.Join(errs...))
	}
	return nil
}

func (ln *LocalNetwork) GetGenesisPath() string {
	return filepath.Join(ln.Dir, "genesis.json")
}

func (ln *LocalNetwork) ReadGenesis() error {
	bytes, err := os.ReadFile(ln.GetGenesisPath())
	if err != nil {
		return fmt.Errorf("failed to read genesis: %w", err)
	}
	genesis := genesis.UnparsedConfig{}
	if err := json.Unmarshal(bytes, &genesis); err != nil {
		return fmt.Errorf("failed to unmarshal genesis: %w", err)
	}
	ln.Genesis = &genesis
	return nil
}

func (ln *LocalNetwork) WriteGenesis() error {
	bytes, err := tmpnet.DefaultJSONMarshal(ln.Genesis)
	if err != nil {
		return fmt.Errorf("failed to marshal genesis: %w", err)
	}
	if err := os.WriteFile(ln.GetGenesisPath(), bytes, perms.ReadWrite); err != nil {
		return fmt.Errorf("failed to write genesis: %w", err)
	}
	return nil
}

func (ln *LocalNetwork) GetChainConfigDir() string {
	return filepath.Join(ln.Dir, "chains")
}

func (ln *LocalNetwork) ReadChainConfigs() error {
	baseChainConfigDir := ln.GetChainConfigDir()
	entries, err := os.ReadDir(baseChainConfigDir)
	if err != nil {
		return fmt.Errorf("failed to read chain config dir: %w", err)
	}

	// Clear the map of data that may end up stale (e.g. if a given
	// chain is in the map but no longer exists on disk)
	ln.ChainConfigs = map[string]tmpnet.FlagsMap{}

	for _, entry := range entries {
		if !entry.IsDir() {
			// Chain config files are expected to be nested under a
			// directory with the name of the chain alias.
			continue
		}
		chainAlias := entry.Name()
		configPath := filepath.Join(baseChainConfigDir, chainAlias, defaultChainConfigFilename)
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			// No config file present
			continue
		}
		chainConfig, err := tmpnet.ReadFlagsMap(configPath, fmt.Sprintf("%s chain config", chainAlias))
		if err != nil {
			return err
		}
		ln.ChainConfigs[chainAlias] = *chainConfig
	}

	return nil
}

func (ln *LocalNetwork) WriteChainConfigs() error {
	baseChainConfigDir := ln.GetChainConfigDir()

	for chainAlias, chainConfig := range ln.ChainConfigs {
		// Create the directory
		chainConfigDir := filepath.Join(baseChainConfigDir, chainAlias)
		if err := os.MkdirAll(chainConfigDir, perms.ReadWriteExecute); err != nil {
			return fmt.Errorf("failed to create %s chain config dir: %w", chainAlias, err)
		}

		// Write the file
		path := filepath.Join(chainConfigDir, defaultChainConfigFilename)
		if err := chainConfig.Write(path, fmt.Sprintf("%s chain config", chainAlias)); err != nil {
			return err
		}
	}

	// TODO(marun) Ensure the removal of chain aliases that aren't present in the map

	return nil
}

// Used to marshal/unmarshal persistent local network defaults.
type localDefaults struct {
	Flags      tmpnet.FlagsMap
	ExecPath   string
	FundedKeys []*secp256k1.PrivateKey
}

func (ln *LocalNetwork) GetDefaultsPath() string {
	return filepath.Join(ln.Dir, "defaults.json")
}

func (ln *LocalNetwork) ReadDefaults() error {
	bytes, err := os.ReadFile(ln.GetDefaultsPath())
	if err != nil {
		return fmt.Errorf("failed to read defaults: %w", err)
	}
	defaults := localDefaults{}
	if err := json.Unmarshal(bytes, &defaults); err != nil {
		return fmt.Errorf("failed to unmarshal defaults: %w", err)
	}
	ln.DefaultFlags = defaults.Flags
	ln.ExecPath = defaults.ExecPath
	ln.FundedKeys = defaults.FundedKeys
	return nil
}

func (ln *LocalNetwork) WriteDefaults() error {
	defaults := localDefaults{
		Flags:      ln.DefaultFlags,
		ExecPath:   ln.ExecPath,
		FundedKeys: ln.FundedKeys,
	}
	bytes, err := tmpnet.DefaultJSONMarshal(defaults)
	if err != nil {
		return fmt.Errorf("failed to marshal defaults: %w", err)
	}
	if err := os.WriteFile(ln.GetDefaultsPath(), bytes, perms.ReadWrite); err != nil {
		return fmt.Errorf("failed to write defaults: %w", err)
	}
	return nil
}

func (ln *LocalNetwork) EnvFilePath() string {
	return filepath.Join(ln.Dir, "network.env")
}

func (ln *LocalNetwork) EnvFileContents() string {
	return fmt.Sprintf("export %s=%s", NetworkDirEnvName, ln.Dir)
}

// Write an env file that sets the network dir env when sourced.
func (ln *LocalNetwork) WriteEnvFile() error {
	if err := os.WriteFile(ln.EnvFilePath(), []byte(ln.EnvFileContents()), perms.ReadWrite); err != nil {
		return fmt.Errorf("failed to write local network env file: %w", err)
	}
	return nil
}

func (ln *LocalNetwork) WriteNodes() error {
	for _, node := range ln.Nodes {
		if err := node.WriteConfig(); err != nil {
			return err
		}
	}
	return nil
}

// Write network configuration to disk.
func (ln *LocalNetwork) WriteAll() error {
	if len(ln.Dir) == 0 {
		return errInvalidNetworkDir
	}
	if err := ln.WriteGenesis(); err != nil {
		return err
	}
	if err := ln.WriteChainConfigs(); err != nil {
		return err
	}
	if err := ln.WriteDefaults(); err != nil {
		return err
	}
	if err := ln.WriteEnvFile(); err != nil {
		return err
	}
	return ln.WriteNodes()
}

// Read network configuration from disk.
func (ln *LocalNetwork) ReadConfig() error {
	if err := ln.ReadGenesis(); err != nil {
		return err
	}
	if err := ln.ReadChainConfigs(); err != nil {
		return err
	}
	return ln.ReadDefaults()
}

// Read node configuration and process context from disk.
func (ln *LocalNetwork) ReadNodes() error {
	nodes, err := ReadNodes(ln.Dir, nil /* skipFunc */)
	if err != nil {
		return err
	}
	ln.Nodes = nodes
	return nil
}

// Read network and node configuration from disk.
func (ln *LocalNetwork) ReadAll() error {
	if err := ln.ReadConfig(); err != nil {
		return err
	}
	return ln.ReadNodes()
}

func (ln *LocalNetwork) AddLocalNode(w io.Writer, node *LocalNode, isEphemeral bool) (*LocalNode, error) {
	// Assume network configuration has been written to disk and is current in memory

	if node == nil {
		// Set an empty data dir so that PopulateNodeConfig will know
		// to set the default of `[network dir]/[node id]`.
		node = NewLocalNode("")
	}

	// Default to a data dir of [network-dir]/[node-ID]
	nodeParentDir := ln.Dir
	if isEphemeral {
		// For an ephemeral node, default to a data dir of [network-dir]/[ephemeral-dir]/[node-ID]
		// to provide a clear separation between nodes that are expected to expose stable API
		// endpoints and those that will live for only a short time (e.g. a node started by a test
		// and stopped on teardown).
		//
		// The data for an ephemeral node is still stored in the file tree rooted at the network
		// dir to ensure that recursively archiving the network dir in CI will collect all node
		// data used for a test run.
		nodeParentDir = filepath.Join(ln.Dir, defaultEphemeralDirName)
	}

	if err := ln.PopulateNodeConfig(node, nodeParentDir); err != nil {
		return nil, err
	}

	bootstrapIPs, bootstrapIDs, err := ln.GetBootstrapIPsAndIDs()
	if err != nil {
		return nil, err
	}

	var (
		// Use dynamic port allocation.
		httpPort    uint16 = 0
		stakingPort uint16 = 0
	)
	node.SetNetworkingConfigDefaults(httpPort, stakingPort, bootstrapIDs, bootstrapIPs)

	if err := node.WriteConfig(); err != nil {
		return nil, err
	}

	err = node.Start(w, ln.ExecPath)
	if err != nil {
		// Attempt to stop an unhealthy node to provide some assurance to the caller
		// that an error condition will not result in a lingering process.
		stopErr := node.Stop()
		if stopErr != nil {
			err = errors.Join(err, stopErr)
		}
		return nil, err
	}

	return node, nil
}

func (ln *LocalNetwork) GetBootstrapIPsAndIDs() ([]string, []string, error) {
	// Collect staking addresses of running nodes for use in bootstrapping a node
	if err := ln.ReadNodes(); err != nil {
		return nil, nil, fmt.Errorf("failed to read local network nodes: %w", err)
	}
	var (
		bootstrapIPs = make([]string, 0, len(ln.Nodes))
		bootstrapIDs = make([]string, 0, len(ln.Nodes))
	)
	for _, node := range ln.Nodes {
		if len(node.StakingAddress) == 0 {
			// Node is not running
			continue
		}

		bootstrapIPs = append(bootstrapIPs, node.StakingAddress)
		bootstrapIDs = append(bootstrapIDs, node.NodeID.String())
	}

	if len(bootstrapIDs) == 0 {
		return nil, nil, errMissingBootstrapNodes
	}

	return bootstrapIPs, bootstrapIDs, nil
}

func (ln *LocalNetwork) GetEphemeralNodes(nodeIDs []ids.NodeID) ([]tmpnet.Node, error) {
	ephemeralDir := filepath.Join(ln.Dir, defaultEphemeralDirName)

	if _, err := os.Stat(ephemeralDir); os.IsNotExist(err) {
		return []tmpnet.Node{}, nil
	} else if err != nil {
		return nil, err
	}

	targetNodeIDs := set.NewSet[string](len(nodeIDs))
	for _, nodeID := range nodeIDs {
		targetNodeIDs.Add(nodeID.String())
	}

	// Read ephemeral nodes targeted for retrieval
	nodes, err := ReadNodes(ephemeralDir, func(nodeID string) bool {
		// Skip nodes not targeted for inclusion if a list of node IDs was provided
		return len(targetNodeIDs) > 0 && !targetNodeIDs.Contains(nodeID)
	})
	if err != nil {
		return nil, err
	}
	return localNodeSliceToNodeSlice(nodes), nil
}

func (ln *LocalNetwork) GetSubnets() ([]*tmpnet.Subnet, error) {
	subnetDir := filepath.Join(ln.Dir, defaultSubnetDirName)

	if _, err := os.Stat(subnetDir); os.IsNotExist(err) {
		return []*tmpnet.Subnet{}, nil
	} else if err != nil {
		return nil, err
	}

	// Node configuration / process context is stored in child directories
	entries, err := os.ReadDir(subnetDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read subnet dir: %w", err)
	}

	subnets := []*tmpnet.Subnet{}
	for _, entry := range entries {
		if entry.IsDir() {
			// Looking only for files
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			// Subnet files should have a .json extension
			continue
		}

		subnetPath := filepath.Join(subnetDir, entry.Name())
		bytes, err := os.ReadFile(subnetPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read subnet file %s: %w", subnetPath, err)
		}
		subnet := &tmpnet.Subnet{}
		if err := json.Unmarshal(bytes, subnet); err != nil {
			return nil, fmt.Errorf("failed to unmarshal subnet from %s: %w", subnetPath, err)
		}

		subnets = append(subnets, subnet)
	}

	return subnets, nil
}

func localNodeSliceToNodeSlice(localNodes []*LocalNode) []tmpnet.Node {
	nodes := make([]tmpnet.Node, 0, len(localNodes))
	for _, localNode := range localNodes {
		nodes = append(nodes, localNode)
	}
	return nodes
}

// Read node configuration and process context from disk.
func ReadNodes(dir string, skipFunc func(nodeID string) bool) ([]*LocalNode, error) {
	nodes := []*LocalNode{}

	// Node configuration / process context is stored in child directories
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read dir: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if skipFunc != nil && skipFunc(entry.Name()) {
			continue
		}

		nodeDir := filepath.Join(dir, entry.Name())
		node, err := ReadNode(nodeDir)
		if errors.Is(err, os.ErrNotExist) {
			// If no config file exists, assume this is not the path of a local node
			continue
		} else if err != nil {
			return nil, err
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

func (ln *LocalNetwork) WriteSubnets(subnets []*tmpnet.Subnet) error {
	subnetDir := filepath.Join(ln.Dir, defaultSubnetDirName)
	if err := os.MkdirAll(subnetDir, perms.ReadWriteExecute); err != nil {
		return fmt.Errorf("failed to create subnet dir: %w", err)
	}

	for _, subnet := range subnets {
		bytes, err := tmpnet.DefaultJSONMarshal(subnet)
		if err != nil {
			return fmt.Errorf("failed to marshal subnet: %w", err)
		}

		subnetPath := filepath.Join(subnetDir, fmt.Sprintf("%s.json", subnet.Spec.Name))
		if err := os.WriteFile(subnetPath, bytes, perms.ReadWrite); err != nil {
			return fmt.Errorf("failed to write subnet: %w", err)
		}
	}
	return nil
}

func (ln *LocalNetwork) RestartSubnets(ctx context.Context, w io.Writer, subnets []*tmpnet.Subnet) error {
	for _, subnet := range subnets {
		nodes, err := subnet.GetNodes(ln)
		if err != nil {
			return fmt.Errorf("failed to retrieve nodes for subnet %s: %w", subnet.Spec.Name, err)
		}
		if _, err := fmt.Fprintf(w, " restarting nodes for subnet %s\n", subnet.Spec.Name); err != nil {
			return err
		}
		for _, node := range nodes {
			bootstrapIPs, bootstrapIDs, err := ln.BootstrapIPsandIDsForNode(node.GetID(), subnets)
			if err != nil {
				return err
			}
			err = node.Restart(ctx, w, ln.ExecPath, bootstrapIPs, bootstrapIDs)
			if err != nil {
				if _, err := fmt.Fprintf(w, " failed to restart node %s: %v\n", node.GetID(), err); err != nil {
					panic(err)
				}
			}
			if _, err := fmt.Fprintf(w, " waiting for node %s to report healthy\n", node.GetID()); err != nil {
				return err
			}
			err = tmpnet.WaitForHealthy(ctx, node)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// TODO(marun) Need to learn more about the semantics of network restart. Initially starting a network
// doesn't require that all nodes be reachable, but for an existing network that clearly is not the case.
func (ln *LocalNetwork) BootstrapIPsandIDsForNode(nodeID ids.NodeID, subnets []*tmpnet.Subnet) ([]string, []string, error) {
	bootstrapIPs, bootstrapIDs, err := ln.GetBootstrapIPsAndIDs()
	if err != nil {
		return nil, nil, err
	}

	// TODO(marun) Unify this with retrieval for all nodes not just subnet nodes
	for _, subnet := range subnets {
		nodes, err := subnet.GetNodes(ln)
		if err != nil {
			return nil, nil, err
		}
		for _, node := range nodes {
			if node.GetID() == nodeID {
				continue
			}
			bootstrapIPs = append(bootstrapIPs, node.GetProcessContext().StakingAddress)
			bootstrapIDs = append(bootstrapIDs, node.GetID().String())
		}
	}
	return bootstrapIPs, bootstrapIDs, nil
}

// Returns staker configuration for the given set of nodes.
func stakersForNodes(networkID uint32, nodes []*LocalNode) ([]genesis.UnparsedStaker, error) {
	// Give staking rewards for initial validators to a random address. Any testing of staking rewards
	// will be easier to perform with nodes other than the initial validators since the timing of
	// staking can be more easily controlled.
	rewardAddr, err := address.Format("X", constants.GetHRP(networkID), ids.GenerateTestShortID().Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to format reward address: %w", err)
	}

	// Configure provided nodes as initial stakers
	initialStakers := make([]genesis.UnparsedStaker, len(nodes))
	for i, node := range nodes {
		pop, err := node.GetProofOfPosession()
		if err != nil {
			return nil, fmt.Errorf("failed to derive proof of possession: %w", err)
		}
		initialStakers[i] = genesis.UnparsedStaker{
			NodeID:        node.NodeID,
			RewardAddress: rewardAddr,
			DelegationFee: .01 * reward.PercentDenominator,
			Signer:        pop,
		}
	}

	return initialStakers, nil
}
