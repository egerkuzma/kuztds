package main

import "testing"

// TestHitPercentBoundaries pins the two ends that the old form got wrong.
// "APIMac.Prob > rand.Intn(100)+1" never fired at prob 1 and fired 99 times in
// 100 at prob 100, because rand.Intn(100)+1 is uniform over [1,100].
func TestHitPercentBoundaries(t *testing.T) {
	const runs = 2000

	for _, p := range []int{0, -5} {
		for i := 0; i < runs; i++ {
			if hitPercent(p) {
				t.Fatalf("hitPercent(%d) fired, want never", p)
			}
		}
	}
	for _, p := range []int{100, 250} {
		for i := 0; i < runs; i++ {
			if !hitPercent(p) {
				t.Fatalf("hitPercent(%d) did not fire, want always", p)
			}
		}
	}
	// prob 1 must be reachable. With the correct form the chance of seeing
	// nothing in 2000 draws is 0.99^2000, about 2e-9.
	hits := 0
	for i := 0; i < runs; i++ {
		if hitPercent(1) {
			hits++
		}
	}
	if hits == 0 {
		t.Fatal("hitPercent(1) never fired in 2000 draws — prob 1 is dead")
	}
}

// TestHitPercentDistribution is a loose sanity check that the middle of the
// range is not shifted by one. At prob 50 over 20000 draws the count sits
// within a few hundred of 10000; the old off-by-one moved it by ~100, which is
// inside the noise, so this only guards against gross errors.
func TestHitPercentDistribution(t *testing.T) {
	const runs = 20000
	hits := 0
	for i := 0; i < runs; i++ {
		if hitPercent(50) {
			hits++
		}
	}
	if hits < runs*45/100 || hits > runs*55/100 {
		t.Fatalf("hitPercent(50) fired %d times in %d, want roughly half", hits, runs)
	}
}
