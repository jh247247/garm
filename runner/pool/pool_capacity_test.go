package pool

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"

	dbMocks "github.com/cloudbase/garm/database/common/mocks"
	"github.com/cloudbase/garm/params"
)

func TestAddRunnerToPoolSkipsJITSetupWhenPoolIsFull(t *testing.T) {
	store := dbMocks.NewStore(t)
	pool := params.Pool{ID: "small", Enabled: true, MaxRunners: 1}
	store.On("ListPoolInstances", mock.Anything, pool.ID).
		Return([]params.Instance{{ID: "existing"}}, nil).Once()

	manager := &basePoolManager{ctx: context.Background(), store: store}
	err := manager.addRunnerToPool(pool, []string{"in_response_to_job=42"})
	if !errors.Is(err, errPoolAtCapacity) {
		t.Fatalf("expected pool-at-capacity error, got %v", err)
	}
}

func TestAddRunnerToPoolFailsClosedWhenCapacityCannotBeRead(t *testing.T) {
	store := dbMocks.NewStore(t)
	pool := params.Pool{ID: "small", Enabled: true, MaxRunners: 1}
	store.On("ListPoolInstances", mock.Anything, pool.ID).
		Return(nil, errors.New("database unavailable")).Once()

	manager := &basePoolManager{ctx: context.Background(), store: store}
	err := manager.addRunnerToPool(pool, nil)
	if err == nil || errors.Is(err, errPoolAtCapacity) {
		t.Fatalf("expected capacity read error, got %v", err)
	}
}
