package pool

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	dbMocks "github.com/cloudbase/garm/database/common/mocks"
	"github.com/cloudbase/garm/params"
)

func TestQueuedJobsForSchedulingUsesAuthoritativeDatabaseRows(t *testing.T) {
	store := dbMocks.NewStore(t)
	entity := params.ForgeEntity{ID: "repo-id", EntityType: params.ForgeEntityTypeRepository}
	newer := params.Job{ID: 2, WorkflowJobID: 102, Status: string(params.JobStatusQueued), CreatedAt: time.Now()}
	older := params.Job{ID: 1, WorkflowJobID: 101, Status: string(params.JobStatusQueued), CreatedAt: newer.CreatedAt.Add(-time.Minute)}
	store.On("ListEntityJobsByStatus", mock.Anything, entity.EntityType, entity.ID, params.JobStatusQueued).
		Return([]params.Job{newer, older}, nil).Once()

	manager := &basePoolManager{
		ctx: context.Background(), entity: entity, store: store,
		jobs: map[int64]params.Job{},
	}
	queued := manager.queuedJobsForScheduling()
	if len(queued) != 2 || queued[0].WorkflowJobID != older.WorkflowJobID || queued[1].WorkflowJobID != newer.WorkflowJobID {
		t.Fatalf("expected authoritative jobs in oldest-first order, got %#v", queued)
	}
}

func TestQueuedJobsForSchedulingUsesEmptyAuthoritativeResult(t *testing.T) {
	store := dbMocks.NewStore(t)
	entity := params.ForgeEntity{ID: "repo-id", EntityType: params.ForgeEntityTypeRepository}
	store.On("ListEntityJobsByStatus", mock.Anything, entity.EntityType, entity.ID, params.JobStatusQueued).
		Return([]params.Job{}, nil).Once()

	manager := &basePoolManager{
		ctx: context.Background(), entity: entity, store: store,
		jobs: map[int64]params.Job{1: {ID: 1, Status: string(params.JobStatusQueued)}},
	}
	if queued := manager.queuedJobsForScheduling(); len(queued) != 0 {
		t.Fatalf("expected an empty authoritative queue, got %#v", queued)
	}
}

func TestQueuedJobsForSchedulingFallsBackToWatcherCacheOnDatabaseError(t *testing.T) {
	store := dbMocks.NewStore(t)
	entity := params.ForgeEntity{ID: "repo-id", EntityType: params.ForgeEntityTypeRepository}
	store.On("ListEntityJobsByStatus", mock.Anything, entity.EntityType, entity.ID, params.JobStatusQueued).
		Return(nil, errors.New("database unavailable")).Once()
	cached := params.Job{ID: 1, WorkflowJobID: 101, Status: string(params.JobStatusQueued)}

	manager := &basePoolManager{
		ctx: context.Background(), entity: entity, store: store,
		jobs: map[int64]params.Job{cached.ID: cached},
	}
	queued := manager.queuedJobsForScheduling()
	if len(queued) != 1 || queued[0].WorkflowJobID != cached.WorkflowJobID {
		t.Fatalf("expected watcher-cache fallback, got %#v", queued)
	}
}
