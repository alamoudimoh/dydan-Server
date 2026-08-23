package realtime

import (
	"fmt"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/alamoudimoh/daydan-server/config"
)

func newTestPool(t *testing.T, maxPerUser int) *ConnectionPool {
	t.Helper()
	return NewConnectionPool(1, &config.RealTimeConfig{
		MaxConnections:        100,
		MaxConnectionsPerUser: maxPerUser,
		StaleThreshold:        0,
	})
}

func newTestConnection(id string, userID int) *Connection {
	return NewConnection(id, 1, userID, nil, nil, zap.NewNop().Sugar())
}

func TestGetStatsTracksConnectionLifecycle(t *testing.T) {
	pool := newTestPool(t, 10)

	stats := pool.GetStats()
	if stats.ActiveConnections != 0 {
		t.Fatalf("expected 0 active connections, got %d", stats.ActiveConnections)
	}

	conn := newTestConnection("conn-1", 42)
	if err := pool.AddConnection(conn); err != nil {
		t.Fatalf("AddConnection failed: %v", err)
	}

	stats = pool.GetStats()
	if stats.ActiveConnections != 1 {
		t.Fatalf("expected 1 active connection, got %d", stats.ActiveConnections)
	}

	pool.BroadcastToUser(42, &Event{Type: "test"})
	stats = pool.GetStats()
	if stats.TotalMessages != 1 {
		t.Fatalf("expected 1 total message, got %d", stats.TotalMessages)
	}
	if stats.QueueSize != 1 {
		t.Fatalf("expected queue size 1, got %d", stats.QueueSize)
	}

	pool.RemoveConnection(conn)
	conn.Close()

	stats = pool.GetStats()
	if stats.ActiveConnections != 0 {
		t.Fatalf("expected 0 active connections after removal, got %d", stats.ActiveConnections)
	}
}

func TestGetStatsSnapshotIsIndependentOfPoolState(t *testing.T) {
	pool := newTestPool(t, 10)

	conn := newTestConnection("conn-1", 1)
	if err := pool.AddConnection(conn); err != nil {
		t.Fatalf("AddConnection failed: %v", err)
	}
	defer func() {
		pool.RemoveConnection(conn)
		conn.Close()
	}()

	snapshot := pool.GetStats()
	snapshot.ActiveConnections = -12345
	snapshot.TotalMessages = -6789

	fresh := pool.GetStats()
	if fresh.ActiveConnections != 1 || fresh.TotalMessages != 0 {
		t.Fatalf("mutating the snapshot leaked into pool state: %+v", fresh)
	}
}

func TestGetStatsConcurrentWithPoolMutations(t *testing.T) {
	pool := newTestPool(t, 8)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				conn := newTestConnection(fmt.Sprintf("conn-%d-%d", n, j), n+1)
				if err := pool.AddConnection(conn); err != nil {
					t.Errorf("AddConnection failed: %v", err)
					continue
				}
				_ = pool.GetStats()
				pool.Broadcast(&Event{Type: "test"})
				pool.RemoveConnection(conn)
				conn.Close()
				_ = pool.GetStats()
			}
		}(i)
	}
	wg.Wait()

	if count := pool.GetConnectionCount(); count != 0 {
		t.Fatalf("expected empty pool after churn, got %d connections", count)
	}
}
