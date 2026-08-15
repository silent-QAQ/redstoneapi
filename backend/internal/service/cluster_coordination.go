package service

import "context"

// ClusterTaskLeaseCoordinator is the narrow boundary used by existing
// background workers. A disabled coordinator must leave their single-node
// behavior unchanged.
type ClusterTaskLeaseCoordinator interface {
	Enabled() bool
	RunWithTaskLease(context.Context, string, func(context.Context) error) (bool, error)
}

// GatewayCacheEpochReader lets a local gateway cache converge after an
// administrator invalidates its namespace from another cluster node.
type GatewayCacheEpochReader interface {
	Enabled() bool
	CacheEpoch(context.Context, string) (int64, error)
}
