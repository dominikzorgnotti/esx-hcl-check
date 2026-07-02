package main

import "testing"

func TestQueryCacheDedup(t *testing.T) {
	calls := 0
	fake := func(programId string, filters []map[string]interface{}, keywords []string, release string) CertStatus {
		calls++
		return CertTrue
	}
	qc := newQueryCache()

	ioFilters := []map[string]interface{}{
		{"displayKey": "vid", "filterValues": []string{"8086"}},
		{"displayKey": "did", "filterValues": []string{"1572"}},
	}

	// Same hardware queried 50 times (as across a 50-host cluster) -> one call.
	for i := 0; i < 50; i++ {
		if got := qc.getWith(fake, "io", ioFilters, nil, "ESXi 9.1"); got != CertTrue {
			t.Fatalf("verdict = %v, want CertTrue", got)
		}
	}
	if calls != 1 {
		t.Fatalf("live calls = %d, want 1 (cache miss once, then hits)", calls)
	}

	// A different release is a different key -> a second live call.
	qc.getWith(fake, "io", ioFilters, nil, "ESXi 8.0 U3")
	if calls != 2 {
		t.Fatalf("live calls = %d, want 2 after a new release key", calls)
	}
}

func TestQueryCacheDoesNotCacheError(t *testing.T) {
	calls := 0
	// Fails the first time, succeeds after — simulating a transient blip.
	fake := func(programId string, filters []map[string]interface{}, keywords []string, release string) CertStatus {
		calls++
		if calls == 1 {
			return CertError
		}
		return CertFalse
	}
	qc := newQueryCache()

	if got := qc.getWith(fake, "server", nil, []string{"PowerEdge R750"}, "ESXi 9.1"); got != CertError {
		t.Fatalf("first verdict = %v, want CertError", got)
	}
	// CertError must not have been cached, so the retry actually re-queries.
	if got := qc.getWith(fake, "server", nil, []string{"PowerEdge R750"}, "ESXi 9.1"); got != CertFalse {
		t.Fatalf("second verdict = %v, want CertFalse (error not cached)", got)
	}
	if calls != 2 {
		t.Fatalf("live calls = %d, want 2 (error result not cached)", calls)
	}
}
