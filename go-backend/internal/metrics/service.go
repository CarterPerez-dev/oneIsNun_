/*
AngelaMos | 2026
service.go
*/

package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/carterperez-dev/templates/go-backend/internal/mongodb"
)

type metricsRepository interface {
	GetServerStatus(ctx context.Context) (*mongodb.ServerStatus, error)
	GetDatabaseStats(ctx context.Context, dbName string) (*mongodb.DatabaseStats, error)
	GetCurrentOps(ctx context.Context) ([]mongodb.Operation, error)
	ListDatabases(ctx context.Context) ([]string, error)
	GetCollectionCount(ctx context.Context, dbName string) (int, error)
}

type Service struct {
	repo     metricsRepository
	database string
}

func NewService(repo metricsRepository, database string) *Service {
	return &Service{
		repo:     repo,
		database: database,
	}
}

type DashboardMetrics struct {
	Timestamp   time.Time       `json:"timestamp"`
	Server      ServerMetrics   `json:"server"`
	Database    DatabaseMetrics `json:"database"`
	Connections ConnectionStats `json:"connections"`
	Operations  OpCounters      `json:"operations"`
	Memory      MemoryStats     `json:"memory"`
	Network     NetworkStats    `json:"network"`
	ActiveOps   int             `json:"active_ops"`
}

type ServerMetrics struct {
	Host      string `json:"host"`
	Version   string `json:"version"`
	UptimeSec int64  `json:"uptime_seconds"`
}

type DatabaseMetrics struct {
	Name            string  `json:"name"`
	Collections     int     `json:"collections"`
	Documents       int64   `json:"documents"`
	DataSizeMB      float64 `json:"data_size_mb"`
	StorageSizeMB   float64 `json:"storage_size_mb"`
	Indexes         int     `json:"indexes"`
	IndexSizeMB     float64 `json:"index_size_mb"`
	TotalDatabases  int     `json:"total_databases"`
}

type ConnectionStats struct {
	Current      int `json:"current"`
	Available    int `json:"available"`
	TotalCreated int `json:"total_created"`
}

type OpCounters struct {
	Insert  int64 `json:"insert"`
	Query   int64 `json:"query"`
	Update  int64 `json:"update"`
	Delete  int64 `json:"delete"`
	Getmore int64 `json:"getmore"`
	Command int64 `json:"command"`
	Total   int64 `json:"total"`
}

type MemoryStats struct {
	ResidentMB int `json:"resident_mb"`
	VirtualMB  int `json:"virtual_mb"`
}

type NetworkStats struct {
	BytesInMB   float64 `json:"bytes_in_mb"`
	BytesOutMB  float64 `json:"bytes_out_mb"`
	NumRequests int64   `json:"num_requests"`
}

func (s *Service) GetDashboardMetrics(ctx context.Context) (*DashboardMetrics, error) {
	serverStatus, err := s.repo.GetServerStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("get server status: %w", err)
	}

	dbStats, err := s.repo.GetDatabaseStats(ctx, s.database)
	if err != nil {
		return nil, fmt.Errorf("get database stats: %w", err)
	}

	databases, err := s.repo.ListDatabases(ctx)
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}

	activeOps, err := s.repo.GetCurrentOps(ctx)
	if err != nil {
		return nil, fmt.Errorf("get current ops: %w", err)
	}

	totalOps := serverStatus.Opcounters.Insert +
		serverStatus.Opcounters.Query +
		serverStatus.Opcounters.Update +
		serverStatus.Opcounters.Delete +
		serverStatus.Opcounters.Getmore +
		serverStatus.Opcounters.Command

	return &DashboardMetrics{
		Timestamp: time.Now(),
		Server: ServerMetrics{
			Host:      serverStatus.Host,
			Version:   serverStatus.Version,
			UptimeSec: serverStatus.Uptime,
		},
		Database: DatabaseMetrics{
			Name:           s.database,
			Collections:    dbStats.Collections,
			Documents:      dbStats.Objects,
			DataSizeMB:     bytesToMB(dbStats.DataSize),
			StorageSizeMB:  bytesToMB(dbStats.StorageSize),
			Indexes:        dbStats.Indexes,
			IndexSizeMB:    bytesToMB(dbStats.IndexSize),
			TotalDatabases: len(databases),
		},
		Connections: ConnectionStats{
			Current:      serverStatus.Connections.Current,
			Available:    serverStatus.Connections.Available,
			TotalCreated: serverStatus.Connections.TotalCreated,
		},
		Operations: OpCounters{
			Insert:  serverStatus.Opcounters.Insert,
			Query:   serverStatus.Opcounters.Query,
			Update:  serverStatus.Opcounters.Update,
			Delete:  serverStatus.Opcounters.Delete,
			Getmore: serverStatus.Opcounters.Getmore,
			Command: serverStatus.Opcounters.Command,
			Total:   totalOps,
		},
		Memory: MemoryStats{
			ResidentMB: serverStatus.Mem.Resident,
			VirtualMB:  serverStatus.Mem.Virtual,
		},
		Network: NetworkStats{
			BytesInMB:   bytesToMB(float64(serverStatus.Network.BytesIn)),
			BytesOutMB:  bytesToMB(float64(serverStatus.Network.BytesOut)),
			NumRequests: serverStatus.Network.NumRequests,
		},
		ActiveOps: len(activeOps),
	}, nil
}

func bytesToMB(bytes float64) float64 {
	return bytes / (1024 * 1024)
}
