// Package cluster implements Redstone's optional multi-node control plane.
package cluster

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/silent-QAQ/redstoneapi/internal/config"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	StateActive   = "active"
	StateDraining = "draining"
	StateOffline  = "offline"
	StateUnknown  = "unknown"

	cacheEpochKeyPrefix = "redstone:cluster:cache-epoch:"
)

var (
	ErrClusterDisabled = errors.New("redstone cluster is disabled")
	ErrNodeDraining    = errors.New("redstone cluster node is draining")
	ErrLeaseNotHeld    = errors.New("redstone cluster lease is not held")
	ErrNodeNotFound    = errors.New("redstone cluster node was not found")
)

type Node struct {
	NodeID           string          `json:"node_id"`
	State            string          `json:"state"`
	AdvertiseURL     string          `json:"advertise_url"`
	Version          string          `json:"version"`
	Capabilities     json.RawMessage `json:"capabilities"`
	LastHeartbeatAt  time.Time       `json:"last_heartbeat_at"`
	DrainRequestedAt *time.Time      `json:"drain_requested_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	Healthy          bool            `json:"healthy"`
}

type TaskLease struct {
	TaskName     string    `json:"task_name"`
	HolderNodeID string    `json:"holder_node_id"`
	LeaseToken   uuid.UUID `json:"lease_token"`
	FencingToken int64     `json:"fencing_token"`
	LeaseUntil   time.Time `json:"lease_until"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Manager struct {
	cfg   config.RedstoneClusterConfig
	db    *sql.DB
	redis *redis.Client

	nodeID       string
	capabilities json.RawMessage

	state atomic.Value

	mu      sync.Mutex
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	started bool
	stopped bool
}

func NewManager(cfg config.RedstoneClusterConfig, db *sql.DB, redisClient *redis.Client) *Manager {
	cfg = normalizeConfig(cfg)
	nodeID := strings.TrimSpace(cfg.NodeID)
	if nodeID == "" {
		nodeID = hostnameOrDefault()
	}

	capabilities, _ := json.Marshal(map[string]bool{
		"cache_epochs": true,
		"gateway":      true,
		"task_leases":  true,
	})
	m := &Manager{
		cfg:          cfg,
		db:           db,
		redis:        redisClient,
		nodeID:       nodeID,
		capabilities: capabilities,
	}
	m.state.Store(StateUnknown)
	return m
}

func ProvideManager(cfg *config.Config, db *sql.DB, redisClient *redis.Client) *Manager {
	if cfg == nil {
		return NewManager(config.RedstoneClusterConfig{}, db, redisClient)
	}
	m := NewManager(cfg.RedstoneCluster, db, redisClient)
	m.Start()
	return m
}

func normalizeConfig(cfg config.RedstoneClusterConfig) config.RedstoneClusterConfig {
	if cfg.HeartbeatIntervalSeconds <= 0 {
		cfg.HeartbeatIntervalSeconds = 10
	}
	if cfg.NodeTimeoutSeconds < cfg.HeartbeatIntervalSeconds*3 {
		cfg.NodeTimeoutSeconds = cfg.HeartbeatIntervalSeconds * 3
	}
	if cfg.LeaseDurationSeconds < cfg.HeartbeatIntervalSeconds*2 {
		cfg.LeaseDurationSeconds = cfg.HeartbeatIntervalSeconds * 2
	}
	if cfg.CacheEpochTTLSeconds <= 0 {
		cfg.CacheEpochTTLSeconds = 60
	}
	return cfg
}

func hostnameOrDefault() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return "redstone-node"
	}
	return strings.TrimSpace(hostname)
}

func (m *Manager) Enabled() bool {
	return m != nil && m.cfg.Enabled
}

func (m *Manager) NodeID() string {
	if m == nil {
		return ""
	}
	return m.nodeID
}

func (m *Manager) State() string {
	if m == nil || !m.Enabled() {
		return StateActive
	}
	state, _ := m.state.Load().(string)
	if state == "" {
		return StateUnknown
	}
	return state
}

// Ready tells a load balancer whether the local process should receive new traffic.
func (m *Manager) Ready() bool {
	return !m.Enabled() || m.State() == StateActive
}

func (m *Manager) Start() {
	if !m.Enabled() || m.db == nil {
		return
	}
	m.mu.Lock()
	if m.started || m.stopped {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.started = true
	m.wg.Add(1)
	m.mu.Unlock()

	m.heartbeatOnce(ctx)
	go m.heartbeatLoop(ctx)
}

func (m *Manager) heartbeatLoop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(time.Duration(m.cfg.HeartbeatIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.heartbeatOnce(ctx)
		}
	}
}

func (m *Manager) heartbeatOnce(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	if err := m.Heartbeat(ctx); err != nil {
		slog.Warn("redstone cluster heartbeat failed", "node_id", m.nodeID, "error", err)
	}
}

// Heartbeat records local liveness while preserving an operator-issued draining state.
func (m *Manager) Heartbeat(ctx context.Context) error {
	if !m.Enabled() {
		return nil
	}
	if m.db == nil {
		return errors.New("redstone cluster database is unavailable")
	}

	var state string
	err := m.db.QueryRowContext(ctx, `
		INSERT INTO redstone_cluster_nodes (
			node_id, state, advertise_url, capabilities, last_heartbeat_at, updated_at
		) VALUES ($1, 'active', $2, $3::jsonb, NOW(), NOW())
		ON CONFLICT (node_id) DO UPDATE SET
			advertise_url = EXCLUDED.advertise_url,
			capabilities = EXCLUDED.capabilities,
			last_heartbeat_at = EXCLUDED.last_heartbeat_at,
			updated_at = EXCLUDED.updated_at
		RETURNING state
	`, m.nodeID, m.cfg.AdvertiseURL, string(m.capabilities)).Scan(&state)
	if err != nil {
		// A previously successful heartbeat must not keep the node routable when
		// the control plane becomes unreachable. The load balancer relies on this
		// fail-closed transition during a PostgreSQL or network outage.
		m.state.Store(StateUnknown)
		return fmt.Errorf("upsert cluster node heartbeat: %w", err)
	}
	m.state.Store(normalizeState(state))
	return nil
}

func (m *Manager) Stop() {
	if m == nil || !m.Enabled() {
		return
	}
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.stopped = true
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()

	ctx, cancelOffline := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelOffline()
	if m.db != nil {
		if _, err := m.db.ExecContext(ctx, `
			UPDATE redstone_cluster_nodes
			SET state = 'offline', updated_at = NOW()
			WHERE node_id = $1 AND state = 'active'
		`, m.nodeID); err != nil {
			slog.Warn("redstone cluster offline transition failed", "node_id", m.nodeID, "error", err)
		}
	}
}

func (m *Manager) ListNodes(ctx context.Context) ([]Node, error) {
	if err := m.requireEnabled(); err != nil {
		return nil, err
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT node_id, state, advertise_url, version, capabilities, last_heartbeat_at,
		       drain_requested_at, created_at, updated_at
		FROM redstone_cluster_nodes
		ORDER BY node_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list cluster nodes: %w", err)
	}
	defer rows.Close()

	nodes := make([]Node, 0)
	deadline := time.Now().UTC().Add(-time.Duration(m.cfg.NodeTimeoutSeconds) * time.Second)
	for rows.Next() {
		var node Node
		var capabilities []byte
		if err := rows.Scan(
			&node.NodeID,
			&node.State,
			&node.AdvertiseURL,
			&node.Version,
			&capabilities,
			&node.LastHeartbeatAt,
			&node.DrainRequestedAt,
			&node.CreatedAt,
			&node.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan cluster node: %w", err)
		}
		node.Capabilities = append(node.Capabilities[:0], capabilities...)
		node.State = normalizeState(node.State)
		node.Healthy = node.State == StateActive && !node.LastHeartbeatAt.Before(deadline)
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cluster nodes: %w", err)
	}
	return nodes, nil
}

func (m *Manager) SetNodeDraining(ctx context.Context, nodeID string) error {
	return m.setNodeState(ctx, nodeID, StateDraining)
}

func (m *Manager) ResumeNode(ctx context.Context, nodeID string) error {
	return m.setNodeState(ctx, nodeID, StateActive)
}

func (m *Manager) setNodeState(ctx context.Context, nodeID, state string) error {
	if err := m.requireEnabled(); err != nil {
		return err
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return ErrNodeNotFound
	}
	result, err := m.db.ExecContext(ctx, `
		UPDATE redstone_cluster_nodes
		SET state = $2,
		    drain_requested_at = CASE WHEN $2 = 'draining' THEN NOW() ELSE NULL END,
		    updated_at = NOW()
		WHERE node_id = $1
	`, nodeID, state)
	if err != nil {
		return fmt.Errorf("set cluster node state: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read cluster node transition result: %w", err)
	}
	if updated == 0 {
		return ErrNodeNotFound
	}
	if nodeID == m.nodeID {
		m.state.Store(state)
	}
	_, err = m.BumpCacheEpoch(ctx, "cluster:nodes")
	return err
}

// AcquireTaskLease returns a fenced, renewable lease for a singleton worker.
func (m *Manager) AcquireTaskLease(ctx context.Context, taskName string) (*TaskLease, error) {
	if err := m.requireActiveNode(ctx); err != nil {
		return nil, err
	}
	taskName = strings.TrimSpace(taskName)
	if taskName == "" {
		return nil, fmt.Errorf("task lease name is required")
	}
	token := uuid.New()
	lease := &TaskLease{TaskName: taskName, HolderNodeID: m.nodeID, LeaseToken: token}
	err := m.db.QueryRowContext(ctx, `
		INSERT INTO redstone_cluster_task_leases (
			task_name, holder_node_id, lease_token, fencing_token, lease_until, updated_at
		) VALUES ($1, $2, $3, 1, NOW() + ($4 * INTERVAL '1 second'), NOW())
		ON CONFLICT (task_name) DO UPDATE SET
			holder_node_id = EXCLUDED.holder_node_id,
			lease_token = EXCLUDED.lease_token,
			fencing_token = redstone_cluster_task_leases.fencing_token + 1,
			lease_until = EXCLUDED.lease_until,
			updated_at = EXCLUDED.updated_at
		WHERE redstone_cluster_task_leases.lease_until <= NOW()
		   OR redstone_cluster_task_leases.holder_node_id = EXCLUDED.holder_node_id
		RETURNING fencing_token, lease_until, updated_at
	`, taskName, m.nodeID, token, m.cfg.LeaseDurationSeconds).Scan(
		&lease.FencingToken,
		&lease.LeaseUntil,
		&lease.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLeaseNotHeld
	}
	if err != nil {
		return nil, fmt.Errorf("acquire task lease: %w", err)
	}
	return lease, nil
}

func (m *Manager) RenewTaskLease(ctx context.Context, lease *TaskLease) error {
	if lease == nil || lease.TaskName == "" || lease.LeaseToken == uuid.Nil {
		return ErrLeaseNotHeld
	}
	if err := m.requireActiveNode(ctx); err != nil {
		return err
	}
	result, err := m.db.ExecContext(ctx, `
		UPDATE redstone_cluster_task_leases
		SET lease_until = NOW() + ($4 * INTERVAL '1 second'), updated_at = NOW()
		WHERE task_name = $1
		  AND holder_node_id = $2
		  AND lease_token = $3
		  AND lease_until > NOW()
	`, lease.TaskName, m.nodeID, lease.LeaseToken, m.cfg.LeaseDurationSeconds)
	if err != nil {
		return fmt.Errorf("renew task lease: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read task lease renewal result: %w", err)
	}
	if updated == 0 {
		return ErrLeaseNotHeld
	}
	lease.LeaseUntil = time.Now().UTC().Add(time.Duration(m.cfg.LeaseDurationSeconds) * time.Second)
	return nil
}

func (m *Manager) ReleaseTaskLease(ctx context.Context, lease *TaskLease) error {
	if lease == nil || lease.TaskName == "" || lease.LeaseToken == uuid.Nil || !m.Enabled() {
		return nil
	}
	_, err := m.db.ExecContext(ctx, `
		DELETE FROM redstone_cluster_task_leases
		WHERE task_name = $1 AND holder_node_id = $2 AND lease_token = $3
	`, lease.TaskName, m.nodeID, lease.LeaseToken)
	if err != nil {
		return fmt.Errorf("release task lease: %w", err)
	}
	return nil
}

// RunWithTaskLease executes work only while this node owns a fenced task lease.
// It renews the lease for the duration of work and cancels the work context if
// the lease can no longer be renewed. Callers must pass the supplied context to
// all upstream and database work so a stale worker stops promptly.
func (m *Manager) RunWithTaskLease(ctx context.Context, taskName string, work func(context.Context) error) (bool, error) {
	if work == nil {
		return false, fmt.Errorf("task lease work is required")
	}
	lease, err := m.AcquireTaskLease(ctx, taskName)
	if errors.Is(err, ErrLeaseNotHeld) || errors.Is(err, ErrNodeDraining) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	renewDone := make(chan struct{})
	renewErr := make(chan error, 1)
	renewEvery := time.Duration(m.cfg.LeaseDurationSeconds) * time.Second / 2
	if renewEvery < time.Second {
		renewEvery = time.Second
	}
	go func() {
		defer close(renewDone)
		ticker := time.NewTicker(renewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-workCtx.Done():
				return
			case <-ticker.C:
				if err := m.RenewTaskLease(workCtx, lease); err != nil {
					select {
					case renewErr <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()

	workErr := work(workCtx)
	cancel()
	<-renewDone

	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 3*time.Second)
	releaseErr := m.ReleaseTaskLease(releaseCtx, lease)
	releaseCancel()

	select {
	case err := <-renewErr:
		return true, fmt.Errorf("renew task lease: %w", err)
	default:
	}
	if workErr != nil {
		return true, workErr
	}
	if releaseErr != nil {
		return true, releaseErr
	}
	return true, nil
}

func (m *Manager) ListTaskLeases(ctx context.Context) ([]TaskLease, error) {
	if err := m.requireEnabled(); err != nil {
		return nil, err
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT task_name, holder_node_id, lease_token, fencing_token, lease_until, updated_at
		FROM redstone_cluster_task_leases
		ORDER BY task_name
	`)
	if err != nil {
		return nil, fmt.Errorf("list task leases: %w", err)
	}
	defer rows.Close()
	leases := make([]TaskLease, 0)
	for rows.Next() {
		var lease TaskLease
		if err := rows.Scan(&lease.TaskName, &lease.HolderNodeID, &lease.LeaseToken, &lease.FencingToken, &lease.LeaseUntil, &lease.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan task lease: %w", err)
		}
		leases = append(leases, lease)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task leases: %w", err)
	}
	return leases, nil
}

// BumpCacheEpoch invalidates a named cache namespace. Database writes are
// authoritative; the Redis copy only makes remote readers converge faster.
func (m *Manager) BumpCacheEpoch(ctx context.Context, cacheName string) (int64, error) {
	if err := m.requireEnabled(); err != nil {
		return 0, err
	}
	cacheName = strings.TrimSpace(cacheName)
	if cacheName == "" {
		return 0, fmt.Errorf("cache epoch name is required")
	}
	var epoch int64
	err := m.db.QueryRowContext(ctx, `
		INSERT INTO redstone_cluster_cache_epochs (cache_name, epoch, invalidated_at)
		VALUES ($1, 1, NOW())
		ON CONFLICT (cache_name) DO UPDATE SET
			epoch = redstone_cluster_cache_epochs.epoch + 1,
			invalidated_at = NOW()
		RETURNING epoch
	`, cacheName).Scan(&epoch)
	if err != nil {
		return 0, fmt.Errorf("bump cache epoch: %w", err)
	}
	m.writeRedisEpoch(ctx, cacheName, epoch)
	return epoch, nil
}

func (m *Manager) CacheEpoch(ctx context.Context, cacheName string) (int64, error) {
	if err := m.requireEnabled(); err != nil {
		return 0, err
	}
	cacheName = strings.TrimSpace(cacheName)
	if cacheName == "" {
		return 0, fmt.Errorf("cache epoch name is required")
	}
	if m.redis != nil {
		if value, err := m.redis.Get(ctx, cacheEpochRedisKey(cacheName)).Int64(); err == nil && value > 0 {
			return value, nil
		}
	}
	var epoch int64
	err := m.db.QueryRowContext(ctx, `
		SELECT epoch FROM redstone_cluster_cache_epochs WHERE cache_name = $1
	`, cacheName).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		m.writeRedisEpoch(ctx, cacheName, 1)
		return 1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read cache epoch: %w", err)
	}
	m.writeRedisEpoch(ctx, cacheName, epoch)
	return epoch, nil
}

func (m *Manager) writeRedisEpoch(ctx context.Context, cacheName string, epoch int64) {
	if m.redis == nil {
		return
	}
	ttl := time.Duration(m.cfg.CacheEpochTTLSeconds) * time.Second
	if err := m.redis.Set(ctx, cacheEpochRedisKey(cacheName), epoch, ttl).Err(); err != nil {
		slog.Debug("redstone cluster cache epoch mirror failed", "cache_name", cacheName, "error", err)
	}
}

func cacheEpochRedisKey(cacheName string) string {
	return cacheEpochKeyPrefix + cacheName
}

func (m *Manager) requireEnabled() error {
	if m == nil || !m.Enabled() {
		return ErrClusterDisabled
	}
	if m.db == nil {
		return errors.New("redstone cluster database is unavailable")
	}
	return nil
}

func (m *Manager) requireActiveNode(ctx context.Context) error {
	if err := m.requireEnabled(); err != nil {
		return err
	}
	var state string
	err := m.db.QueryRowContext(ctx, `
		SELECT state FROM redstone_cluster_nodes WHERE node_id = $1
	`, m.nodeID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNodeNotFound
	}
	if err != nil {
		return fmt.Errorf("check local cluster node state: %w", err)
	}
	state = normalizeState(state)
	m.state.Store(state)
	if state == StateDraining {
		return ErrNodeDraining
	}
	if state != StateActive {
		return ErrLeaseNotHeld
	}
	return nil
}

func normalizeState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case StateActive, StateDraining, StateOffline:
		return strings.ToLower(strings.TrimSpace(state))
	default:
		return StateUnknown
	}
}
