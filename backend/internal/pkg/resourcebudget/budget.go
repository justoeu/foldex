package resourcebudget

// BackgroundWorkerConcurrency is the per-pool ceiling for outbound background
// work. Each constructor applies it independently as defense in depth.
const BackgroundWorkerConcurrency = 8

// PoolMaxConns is the backend pgx ceiling (internal/db). Preview and
// change-check together may take at most half, so HTTP keeps ≥50%.
const PoolMaxConns = 16

// HTTPHeadroom is the connection budget reserved for request handling:
// at least half of the pool, rounded down.
func HTTPHeadroom(poolMaxConns int) int {
	if poolMaxConns < 2 {
		return 1
	}
	return poolMaxConns / 2
}

// FitBackgroundWorkers clamps each worker to BackgroundWorkerConcurrency
// then scales the pair so their sum never exceeds HTTPHeadroom(poolMaxConns).
// Callers still floor each knob (preview ≥1, change-check default 2) before
// this; constructors keep the per-pool clamp as defense in depth.
func FitBackgroundWorkers(preview, changecheck, poolMaxConns int) (int, int) {
	preview = capAt(preview, BackgroundWorkerConcurrency)
	changecheck = capAt(changecheck, BackgroundWorkerConcurrency)
	if preview < 1 {
		preview = 1
	}
	if changecheck < 1 {
		changecheck = 1
	}
	budget := HTTPHeadroom(poolMaxConns)
	if preview+changecheck <= budget {
		return preview, changecheck
	}
	total := preview + changecheck
	p := budget * preview / total
	if p < 1 {
		p = 1
	}
	c := budget - p
	if c < 1 {
		c = 1
		p = budget - 1
	}
	return p, c
}

func capAt(n, max int) int {
	if n > max {
		return max
	}
	return n
}
