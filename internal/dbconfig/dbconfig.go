// Package dbconfig provides the shared DBConfig struct used by both the server
// and ingestor binaries for SQLite vacuum and maintenance settings (#919, #921).
package dbconfig

// DBConfig controls SQLite vacuum and maintenance behavior (#919).
type DBConfig struct {
	// Synchronous selects FULL by default or explicit NORMAL for the ingestor.
	// NORMAL can lose recent committed transactions after a power failure.
	Synchronous            string `json:"synchronous,omitempty"`
	// BatchCommitMs groups the ingestor's writes into one commit per interval
	// (1-4000 ms; 0/unset = one commit per write). Cuts SSD writes several-fold
	// on busy feeds; a crash or power loss can drop up to one interval.
	BatchCommitMs int `json:"batchCommitMs,omitempty"`
	// WalAutocheckpointPages raises SQLite's 1000-page WAL auto-checkpoint
	// with batch commits on, so a page rewritten by many batches is copied
	// into the database once per checkpoint. 0/unset keeps 1000.
	WalAutocheckpointPages int `json:"walAutocheckpointPages,omitempty"`
	VacuumOnStartup        bool   `json:"vacuumOnStartup"`        // one-time full VACUUM on startup if auto_vacuum is not INCREMENTAL
	IncrementalVacuumPages int    `json:"incrementalVacuumPages"` // pages returned to OS per reaper cycle (default 1024)
	AnalysisLimit          int    `json:"analysisLimit"`          // index rows ANALYZE visits per index (default 10000); negative disables the planner stats refresh (#2058)

	// Load controls chunked startup loading (#1009).
	Load *LoadConfig `json:"load,omitempty"`
}

// LoadConfig controls the chunked startup-load behavior (#1009).
type LoadConfig struct {
	// ChunkSize is the number of transmission rows fetched per chunk
	// during PacketStore.LoadChunked. 0/unset → 10000.
	ChunkSize int `json:"chunkSize"`
}

// GetIncrementalVacuumPages returns the configured pages or 1024 default.
func (c *DBConfig) GetIncrementalVacuumPages() int {
	if c != nil && c.IncrementalVacuumPages > 0 {
		return c.IncrementalVacuumPages
	}
	return 1024
}

// GetLoadChunkSize returns the configured chunk size or 10000 default (#1009).
func (c *DBConfig) GetLoadChunkSize() int {
	if c != nil && c.Load != nil && c.Load.ChunkSize > 0 {
		return c.Load.ChunkSize
	}
	return 10000
}
