package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetParallelism(t *testing.T) {
	for i := 1; i < 7; i++ {
		p1, p2 := getParallelism(i)
		assert.GreaterOrEqual(t, p1, 1)
		assert.GreaterOrEqual(t, p2, 1)
		if i > 1 {
			assert.LessOrEqual(t, p1+p2, i)
		}
	}
}
