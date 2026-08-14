package slug

import "fmt"

// Allocator reserves globally unique candidates from a preloaded set without
// making one availability query per suffix.
type Allocator struct {
	used map[string]struct{}
}

func NewAllocator(used []string) *Allocator {
	a := &Allocator{used: make(map[string]struct{}, len(used))}
	for _, candidate := range used {
		a.used[candidate] = struct{}{}
	}
	return a
}

func (a *Allocator) Allocate(base string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("unique slug: empty base")
	}
	for attempt := 1; attempt < MaxUniqueAttempts; attempt++ {
		candidate := base
		if attempt > 1 {
			candidate = fmt.Sprintf("%s-%d", base, attempt)
		}
		if _, taken := a.used[candidate]; taken {
			continue
		}
		a.used[candidate] = struct{}{}
		return candidate, nil
	}
	return "", fmt.Errorf("unique slug: exhausted attempts for %q", base)
}

func (a *Allocator) Release(candidate string) {
	delete(a.used, candidate)
}
