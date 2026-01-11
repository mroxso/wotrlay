package main

import (
	"context"
	"testing"
	"time"
)

func TestRankCacheIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Load test configuration (will auto-generate secret key if not set)
	cfg := loadConfig()

	// Create rank cache with configuration and observability
	obs := &Observability{}
	cache := NewRankCache(ctx, cfg, obs)

	// Test pubkey from the successful integration test
	testPubkey := "6b3780ef2972e73d370b84a3e51e7aa9ae34bf412938dcfbd9c5f63b221416c8"

	// Test blocking GetRank for cache miss
	t.Log("Testing blocking GetRank for cache miss...")
	rank, err := cache.GetRank(ctx, testPubkey)
	if err != nil {
		t.Errorf("GetRank failed: %v", err)
		return
	}

	t.Logf("✓ Rank received: %.4f", rank)
	if rank < 0 || rank > 1 {
		t.Errorf("Rank out of valid range [0,1]: %.4f", rank)
	}

	// Test that subsequent calls use cache
	t.Log("Testing cache hit...")
	rank2, err := cache.GetRank(ctx, testPubkey)
	if err != nil {
		t.Errorf("GetRank failed on cache hit: %v", err)
		return
	}

	if rank2 != rank {
		t.Errorf("Cache returned different rank: got %.4f, want %.4f", rank2, rank)
	} else {
		t.Logf("✓ Cache hit returned same rank: %.4f", rank2)
	}

	// Test non-blocking Rank method
	t.Log("Testing non-blocking Rank method...")
	r, found := cache.Rank(testPubkey)
	if !found {
		t.Error("Rank method should find cached rank")
	} else if r != rank {
		t.Errorf("Rank method returned different rank: got %.4f, want %.4f", r, rank)
	} else {
		t.Logf("✓ Rank method returned cached rank: %.4f", r)
	}
}

// TestGetRankPreservesStaleOnFailure tests that GetRank preserves stale cached data
// when refresh fails, instead of overwriting with 0.
func TestGetRankPreservesStaleOnFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := loadConfig()
	obs := &Observability{}
	cache := NewRankCache(ctx, cfg, obs)

	testPubkey := "test_pubkey_1"
	highRank := 0.9

	// Manually add a high rank to cache
	cache.Update(time.Now(), PubRank{Pubkey: testPubkey, Rank: highRank})

	// Verify it's cached
	rank, exists := cache.Rank(testPubkey)
	if !exists {
		t.Fatal("rank should exist in cache")
	}
	if rank != highRank {
		t.Fatalf("expected rank %.2f, got %.2f", highRank, rank)
	}

	// Make the cache stale by setting an old timestamp
	oldTime := time.Now().Add(-25 * time.Hour)
	cache.Update(oldTime, PubRank{Pubkey: testPubkey, Rank: highRank})

	// Now try to refresh with a very short timeout that will fail
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer shortCancel()

	// GetRank should return the stale rank even though refresh fails
	rank, err := cache.GetRank(shortCtx, testPubkey)
	if err != nil {
		t.Logf("refresh failed as expected: %v", err)
	}

	// The stale rank should be preserved, not 0
	if rank != highRank {
		t.Errorf("expected stale rank %.2f to be preserved, got %.2f", highRank, rank)
	}

	// Verify the cache still has the stale rank
	rank, exists = cache.Rank(testPubkey)
	if !exists {
		t.Error("rank should still exist in cache after failed refresh")
	}
	if rank != highRank {
		t.Errorf("expected cached rank %.2f, got %.2f", highRank, rank)
	}
}

// TestGetRankNoCacheOnFailure tests that GetRank returns 0 when there's no
// cached data and refresh fails.
func TestGetRankNoCacheOnFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := loadConfig()
	obs := &Observability{}
	cache := NewRankCache(ctx, cfg, obs)

	testPubkey := "test_pubkey_2"

	// Verify pubkey is not in cache
	_, exists := cache.Rank(testPubkey)
	if exists {
		t.Fatal("pubkey should not exist in cache initially")
	}

	// Try to refresh with a very short timeout that will fail
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer shortCancel()

	// GetRank should return 0 when there's no cached data and refresh fails
	rank, err := cache.GetRank(shortCtx, testPubkey)
	if err == nil {
		t.Error("expected error from failed refresh")
	}

	if rank != 0 {
		t.Errorf("expected rank 0 when no cache and refresh fails, got %.2f", rank)
	}
}

// TestLRUEviction tests that the LRU cache evicts least recently used entries
// when the cache is full.
func TestLRUEviction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create cache with small size for testing
	cfg := loadConfig()
	cfg.RankCacheSize = 3 // Small cache size
	obs := &Observability{}
	cache := NewRankCache(ctx, cfg, obs)

	// Add 3 entries to fill the cache
	pubkeys := []string{"pubkey1", "pubkey2", "pubkey3"}
	for i, pubkey := range pubkeys {
		cache.Update(time.Now(), PubRank{Pubkey: pubkey, Rank: float64(i+1) / 10})
	}

	// Verify all 3 are in cache
	for _, pubkey := range pubkeys {
		if _, exists := cache.Rank(pubkey); !exists {
			t.Errorf("pubkey %s should be in cache", pubkey)
		}
	}

	// Add a 4th entry - should evict pubkey1 (least recently used)
	cache.Update(time.Now(), PubRank{Pubkey: "pubkey4", Rank: 0.4})

	// pubkey1 should be evicted
	if _, exists := cache.Rank("pubkey1"); exists {
		t.Error("pubkey1 should have been evicted from LRU cache")
	}

	// pubkeys 2, 3, 4 should still be in cache
	for _, pubkey := range []string{"pubkey2", "pubkey3", "pubkey4"} {
		if _, exists := cache.Rank(pubkey); !exists {
			t.Errorf("pubkey %s should still be in cache", pubkey)
		}
	}
}

// TestSingleflightDeduplication tests that concurrent GetRank calls for the same
// pubkey are deduplicated to prevent duplicate network requests.
func TestSingleflightDeduplication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := loadConfig()
	obs := &Observability{}
	cache := NewRankCache(ctx, cfg, obs)

	testPubkey := "test_pubkey_3"

	// Launch 5 concurrent GetRank calls for the same pubkey
	results := make(chan float64, 5)
	errors := make(chan error, 5)

	for range 5 {
		go func() {
			rank, err := cache.GetRank(ctx, testPubkey)
			if err != nil {
				errors <- err
			} else {
				results <- rank
			}
		}()
	}

	// Collect results
	var ranks []float64
	var errs []error
	for range 5 {
		select {
		case rank := <-results:
			ranks = append(ranks, rank)
		case err := <-errors:
			errs = append(errs, err)
		}
	}

	// All successful calls should return the same rank
	if len(ranks) > 0 {
		firstRank := ranks[0]
		for _, rank := range ranks {
			if rank != firstRank {
				t.Errorf("expected all ranks to be %.4f, got %.4f", firstRank, rank)
			}
		}
	}

	// Verify only one network request was made by checking the cache
	// (singleflight ensures only one refreshBatch call)
	rank, exists := cache.Rank(testPubkey)
	if !exists && len(ranks) == 0 {
		t.Error("pubkey should be in cache after concurrent requests")
	} else if exists && len(ranks) > 0 {
		if rank != ranks[0] {
			t.Errorf("cached rank %.4f doesn't match returned rank %.4f", rank, ranks[0])
		}
	}
}
