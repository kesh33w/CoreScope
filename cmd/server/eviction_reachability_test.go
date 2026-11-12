package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Every index must relinquish evicted packets even when resolved paths were
// reconstructed in memory, differ from SQL, or the optional index is disabled.
func TestEvictionDropsReconstructedNodeReferences(t *testing.T) {
	for _, mode := range []string{"cold", "live", "index-disabled", "partial-sql"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now().UTC()
			s := makeTestStore(8, now.Add(-30*time.Minute), 0)
			s.retentionHours = 1
			s.useResolvedPathIndex = mode != "index-disabled"
			s.initResolvedPathIndex()
			s.byPathHop = make(map[string][]*StoreTx)
			shared, expired := strings.Repeat("a", 64), strings.Repeat("b", 64)
			for i, tx := range s.packets {
				pks := []string{shared}
				if i < 2 {
					tx.FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
					pks = append(pks, expired)
				}
				if mode == "cold" {
					s.indexColdLoadRelayHops(tx, pks, nil, nil)
				} else {
					s.indexResolvedPathHops(tx, pks, map[string]bool{}, nil, nil)
				}
			}
			s.noteCertainHeard(expired, s.packets[0])
			var got int
			if mode == "partial-sql" {
				got = s.EvictStaleWithRP(map[int][]string{1: {shared}})
			} else {
				got = s.RunEviction() // No DB: in-memory cleanup must stand alone.
			}
			if got != 2 {
				t.Fatalf("evicted %d, want 2", got)
			}
			if len(s.byNode[expired]) != 0 || len(s.nodeHashes[expired]) != 0 {
				t.Errorf("expired-only reconstructed node keeps %d packets and %d memberships", len(s.byNode[expired]), len(s.nodeHashes[expired]))
			}
			if _, exists := s.certainHeard[expired]; exists {
				t.Error("expired-only node keeps liveness evidence")
			}
			if len(s.byNode[shared]) != 6 || len(s.nodeHashes[shared]) != 6 {
				t.Errorf("shared reconstructed node keeps %d packets and %d memberships, want 6", len(s.byNode[shared]), len(s.nodeHashes[shared]))
			}
			for key, list := range s.byNode {
				for _, tx := range list {
					if s.byTxID[tx.ID] != tx {
						t.Errorf("node %q retains evicted tx %d", key, tx.ID)
					}
				}
			}
			for _, tx := range s.packets {
				tx.FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
			}
			if got := s.RunEviction(); got != 6 {
				t.Fatalf("final eviction = %d, want 6", got)
			}
			if len(s.byNode) != 0 || len(s.nodeHashes) != 0 || len(s.certainHeard) != 0 {
				t.Errorf("empty store retains node indexes: nodes=%d memberships=%d liveness=%d", len(s.byNode), len(s.nodeHashes), len(s.certainHeard))
			}
		})
	}
}

// Late-arriving observations make index order differ from packet time order.
// Keep the old backing arrays visible to check GC reachability, not just len.
func TestEvictionClearsPointerSliceTails(t *testing.T) {
	now := time.Now().UTC()
	s := makeTestStore(8, now.Add(-30*time.Minute), 0)
	s.retentionHours = 1
	for _, tx := range s.packets[:2] {
		tx.FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
	}
	for _, list := range s.byObserver {
		slices.Reverse(list)
	}
	for _, list := range s.byPayloadType {
		slices.Reverse(list)
	}
	slices.Reverse(s.distHops)
	slices.Reverse(s.distPaths)
	txArrays := map[string][]*StoreTx{"packets": s.packets[:cap(s.packets)]}
	obsArrays := map[string][]*StoreObs{}
	for key, list := range s.byObserver {
		obsArrays[key] = list[:cap(list)]
	}
	for key, list := range s.byPayloadType {
		txArrays[fmt.Sprint("payload-", key)] = list[:cap(list)]
	}
	// A shared decoded sender exercises a non-empty byNode bucket.
	shared := "shared"
	for _, tx := range s.packets {
		tx.DecodedJSON = `{"pubKey":"shared"}`
		s.addToByNode(tx, shared)
	}
	slices.Reverse(s.byNode[shared])
	txArrays["node"] = s.byNode[shared][:cap(s.byNode[shared])]
	check := func() {
		t.Helper()
		for name, list := range txArrays {
			for _, tx := range list {
				if tx != nil && s.byTxID[tx.ID] != tx {
					t.Errorf("%s backing array retains evicted tx %d", name, tx.ID)
					break
				}
			}
		}
		for name, list := range obsArrays {
			for _, obs := range list {
				if obs != nil && s.byObsID[obs.ID] != obs {
					t.Errorf("observer %s backing array retains evicted observation %d", name, obs.ID)
					break
				}
			}
		}
		for _, h := range s.distHops[:cap(s.distHops)] {
			if h.tx != nil && s.byTxID[h.tx.ID] != h.tx {
				t.Error("distance-hop backing array retains evicted packet")
				break
			}
		}
		for _, p := range s.distPaths[:cap(s.distPaths)] {
			if p.tx != nil && s.byTxID[p.tx.ID] != p.tx {
				t.Error("distance-path backing array retains evicted packet")
				break
			}
		}
	}
	if got := s.RunEviction(); got != 2 {
		t.Fatalf("evicted %d, want 2", got)
	}
	check()
	for _, tx := range s.packets {
		tx.FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
	}
	if got := s.RunEviction(); got != 6 {
		t.Fatalf("final eviction %d, want 6", got)
	}
	check()
}

// Opt-in accelerated steady-state workload: 20 cache turnovers, 640k
// observations. All rows exist only in this test; no production DB is opened.
// GC is forced only for measurement, never as the production fix.
func TestEvictionSustainedHeapDiagnostic(t *testing.T) {
	if os.Getenv("CORESCOPE_EVICTION_SOAK") != "1" {
		t.Skip("set CORESCOPE_EVICTION_SOAK=1 for retained-heap workload")
	}
	const cycles, batch, perTx = 20, 1000, 32
	now := time.Now().UTC()
	fresh, old := now.Add(-30*time.Minute).Format(time.RFC3339), now.Add(-2*time.Hour).Format(time.RFC3339)
	s := makeTestStore(0, now, 0)
	s.retentionHours = 1
	keys := make([]string, 100)
	for i := range keys {
		keys[i] = fmt.Sprintf("%064x", i+1)
	}
	var first, last runtime.MemStats
	for cycle := 0; cycle < cycles; cycle++ {
		for _, tx := range s.packets {
			tx.FirstSeen = old
		}
		for i := 0; i < batch; i++ {
			id := cycle*batch + i + 1
			tx := &StoreTx{ID: id, Hash: fmt.Sprintf("%016x", id), FirstSeen: fresh}
			for j := 0; j < perTx; j++ {
				oid := id*perTx + j
				obs := &StoreObs{ID: oid, TransmissionID: id, ObserverID: keys[j], PathJSON: fmt.Sprintf(`["%02x","%02x","%02x"]`, i%256, j, cycle), Timestamp: fmt.Sprintf("2026-11-11T%02d:%02d:%02dZ", cycle, i%60, j)}
				tx.addObservation(obs)
				s.byObsID[oid] = obs
				s.byObserver[obs.ObserverID] = append(s.byObserver[obs.ObserverID], obs)
				s.totalObs++
				s.trackedBytes += estimateStoreObsBytes(obs)
			}
			s.byHash[tx.Hash], s.byTxID[id] = tx, tx
			s.packets = append(s.packets, tx)
			s.indexColdLoadRelayHops(tx, keys[i%100:i%100+1], nil, nil)
			s.trackedBytes += rechargeTx(tx)
		}
		got := s.RunEviction()
		want := batch
		if cycle == 0 {
			want = 0
		}
		if got != want || len(s.packets) != batch || s.totalObs != batch*perTx {
			t.Fatalf("workload invariant cycle %d: evicted=%d packets=%d obs=%d", cycle, got, len(s.packets), s.totalObs)
		}
		runtime.GC()
		runtime.ReadMemStats(&last)
		if cycle == 2 {
			first = last
		}
		associations := 0
		for _, list := range s.byNode {
			associations += len(list)
		}
		t.Logf("cycle=%02d packets=%d obs=%d nodeReferences=%d heapMiB=%.2f trackedMiB=%.2f", cycle+1, len(s.packets), s.totalObs, associations, float64(last.HeapAlloc)/(1<<20), s.trackedMemoryMB())
	}
	if path := os.Getenv("CORESCOPE_EVICTION_HEAP_PROFILE"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.WriteHeapProfile(f); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	runtime.KeepAlive(s)
	growth := int64(last.HeapAlloc) - int64(first.HeapAlloc)
	t.Logf("steady-state retained heap growth=%.2f MiB", float64(growth)/(1<<20))
	if growth > 8<<20 {
		t.Fatalf("retained heap grows despite fixed 1000 tx / 32000 obs window: %.2f MiB", float64(growth)/(1<<20))
	}
}

func TestIndexUpdatesClearTailsBeforeEviction(t *testing.T) {
	now := time.Now().UTC()
	s := makeTestStore(8, now.Add(-30*time.Minute), 0)
	s.retentionHours = 1
	s.nodeCacheTime = now
	s.nodePM = buildPrefixMap(nil)
	oldHops, oldPaths := slices.Clone(s.distHops), slices.Clone(s.distPaths)
	hopsSnapshot, pathsSnapshot := s.distHops, s.distPaths
	last := s.packets[len(s.packets)-1]
	// With no node coordinates, replacing these records produces no new ones.
	s.updateDistanceIndexForTxs([]*StoreTx{last})
	if len(s.distHops) != 7 || len(s.distPaths) != 7 {
		t.Fatal("fixture did not shrink distance indexes")
	}
	if s.distHops[:cap(s.distHops)][7].tx != nil || s.distPaths[:cap(s.distPaths)][7].tx != nil {
		t.Error("distance update left an obsolete record beyond len")
	}
	list := append([]*StoreTx(nil), s.packets...)
	idx := map[string][]*StoreTx{"aa": list}
	removeTxFromSlice(idx, "aa", last)
	if list[7] != nil {
		t.Error("path-hop update left an obsolete pointer beyond len")
	}
	s.byPathHop = idx
	for _, tx := range s.packets {
		tx.FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
	}
	s.RunEviction()
	if !slices.EqualFunc(hopsSnapshot, oldHops, func(a, b distHopRecord) bool { return a.tx == b.tx && a.Hash == b.Hash }) ||
		!slices.EqualFunc(pathsSnapshot, oldPaths, func(a, b distPathRecord) bool { return a.tx == b.tx && a.Hash == b.Hash }) {
		t.Error("distance writer mutated a reader's existing snapshot")
	}
	for _, h := range s.distHops[:cap(s.distHops)] {
		if h.tx != nil {
			t.Error("eviction after index shrink retains distance-hop packet")
			break
		}
	}
	for _, p := range s.distPaths[:cap(s.distPaths)] {
		if p.tx != nil {
			t.Error("eviction after index shrink retains distance-path packet")
			break
		}
	}
	for _, tx := range list {
		if tx != nil {
			t.Error("eviction after index shrink retains path-hop packet")
			break
		}
	}
}

// Run with -race: distance writers must replace, not mutate, arrays that
// analytics readers retain as snapshots after releasing RLock.
func TestDistanceAnalyticsConcurrentEviction(t *testing.T) {
	for _, area := range []string{"", "test"} {
		t.Run("area="+area, func(t *testing.T) {
			for cycle := 0; cycle < 8; cycle++ {
				now := time.Now().UTC()
				s := makeTestStore(1000, now.Add(-2*time.Hour), 0)
				s.retentionHours = 1
				s.config = &Config{Areas: map[string]AreaEntry{"test": {}}}
				s.areaNodeCache = map[string]map[string]bool{"test": {"pk0000": true}}
				s.areaNodeCacheTimes = map[string]time.Time{"test": now}
				start, done := make(chan struct{}), make(chan struct{})
				go func() {
					defer close(done)
					<-start
					for i := 0; i < 5; i++ {
						s.computeAnalyticsDistance("", area)
					}
				}()
				close(start)
				runtime.Gosched()
				if got := s.RunEviction(); got != 1000 {
					t.Fatalf("evicted %d, want 1000", got)
				}
				<-done
			}
		})
	}
}

func BenchmarkEvictionNodeSweep(b *testing.B) {
	for _, count := range []int{20000, 160000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			for iteration := 0; iteration < b.N; iteration++ {
				b.StopTimer()
				now := time.Now().UTC()
				s := makeTestStore(count, now.Add(-30*time.Minute), 0)
				s.retentionHours = 1
				keys := make([]string, 2000)
				for i := range keys {
					keys[i] = fmt.Sprintf("%064x", i)
				}
				for i, tx := range s.packets {
					if i < count/20 {
						tx.FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
					}
					for j := 0; j < 8; j++ {
						s.addToByNode(tx, keys[(i+j*13)%len(keys)])
					}
				}
				b.StartTimer()
				got := s.RunEviction()
				b.StopTimer()
				if got != count/20 {
					b.Fatalf("evicted %d, want %d", got, count/20)
				}
				for _, list := range s.byNode {
					for _, tx := range list {
						if s.byTxID[tx.ID] != tx {
							b.Fatal("stale node reference")
						}
					}
				}
			}
			b.ReportMetric(float64(count*8), "relay-memberships/batch")
		})
	}
}

func TestEvictionReleasesGroupedCache(t *testing.T) {
	now := time.Now().UTC()
	s := makeTestStore(4, now.Add(-30*time.Minute), 0)
	s.retentionHours = 1
	if got := s.QueryGroupedPackets(PacketQuery{}).Total; got != 4 {
		t.Fatalf("warm grouped total=%d, want 4", got)
	}
	// A returned snapshot is allowed to finish after eviction. Invalidating the
	// cache must release its root without clearing an active reader's array.
	reader := s.groupedCacheTxs
	s.groupedCacheExp = now.Add(-time.Second)
	s.packets[0].FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
	s.RunEviction()
	if s.groupedCacheTxs != nil {
		t.Error("eviction retained the expired grouped packet cache")
	}
	if len(reader) != 4 || reader[0].tx == nil {
		t.Error("cache invalidation corrupted a borrowed grouped snapshot")
	}
	if got := s.QueryGroupedPackets(PacketQuery{}).Total; got != 3 {
		t.Errorf("grouped total after eviction=%d, want 3", got)
	}
}

func TestGroupedCacheRejectsPreEvictionSnapshot(t *testing.T) {
	now := time.Now().UTC()
	s := makeTestStore(4, now.Add(-30*time.Minute), 0)
	s.retentionHours = 1
	s.QueryGroupedPackets(PacketQuery{})
	oldSnapshot := s.groupedCacheTxs
	oldGeneration := atomic.LoadInt64(&s.evicted)
	s.packets[0].FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
	s.RunEviction()
	// A query that captured its pointers before eviction can finish sorting
	// afterwards. Exercise its real publication step in that exact order.
	s.cacheGroupedPackets(oldSnapshot, "retired", oldGeneration)
	if s.groupedCacheTxs != nil {
		t.Fatal("late grouped-cache publication retained a pre-eviction snapshot")
	}
	s.cacheGroupedPackets([]groupedEntry{{tx: s.packets[0]}, {tx: s.packets[1]}, {tx: s.packets[2]}}, "live", atomic.LoadInt64(&s.evicted))
	if len(s.groupedCacheTxs) != 3 || s.groupedCacheKey != "live" {
		t.Fatal("current grouped snapshot was not published")
	}
}
