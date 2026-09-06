package resourcebudget

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFitBackgroundWorkersKeepsHTTPHeadroom(t *testing.T) {
	p, c := FitBackgroundWorkers(8, 8, PoolMaxConns)
	assert.LessOrEqual(t, p+c, HTTPHeadroom(PoolMaxConns))
	assert.GreaterOrEqual(t, p, 1)
	assert.GreaterOrEqual(t, c, 1)
	assert.Equal(t, 4, p)
	assert.Equal(t, 4, c)
}

func TestFitBackgroundWorkersKeepsDefaults(t *testing.T) {
	p, c := FitBackgroundWorkers(4, 2, PoolMaxConns)
	assert.Equal(t, 4, p)
	assert.Equal(t, 2, c)
}

func TestHTTPHeadroomIsHalfThePool(t *testing.T) {
	assert.Equal(t, 8, HTTPHeadroom(PoolMaxConns))
	assert.Equal(t, PoolMaxConns, 16)
}
