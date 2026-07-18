package pool

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-github/v72/github"
	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"

	dbMocks "github.com/cloudbase/garm/database/common/mocks"
	"github.com/cloudbase/garm/params"
	runnerMocks "github.com/cloudbase/garm/runner/common/mocks"
)

func staleTestManager(t *testing.T, job params.Job) (*basePoolManager, *dbMocks.Store, *runnerMocks.GithubClient) {
	t.Helper()
	store := dbMocks.NewStore(t)
	gh := runnerMocks.NewGithubClient(t)
	entity := params.ForgeEntity{ID: "repo-id", EntityType: params.ForgeEntityTypeRepository}
	store.On("ListEntityJobsByStatus", mock.Anything, entity.EntityType, entity.ID, params.JobStatusQueued).
		Return([]params.Job{job}, nil).Once()
	store.On("ListEntityJobsByStatus", mock.Anything, entity.EntityType, entity.ID, params.JobStatusInProgress).
		Return([]params.Job{}, nil).Once()
	return &basePoolManager{
		ctx: context.Background(), entity: entity, store: store, ghcli: gh,
		staleJobChecks: map[int64]time.Time{}, jobs: map[int64]params.Job{job.ID: job},
	}, store, gh
}

func TestReconcileStaleWorkflowJobsDeletesCompletedJob(t *testing.T) {
	job := params.Job{
		WorkflowJobID: 42, Status: string(params.JobStatusQueued),
		RepositoryOwner: "itembase-app", RepositoryName: "itembase",
		CreatedAt: time.Now().Add(-11 * time.Minute),
	}
	manager, store, gh := staleTestManager(t, job)
	gh.On("GetWorkflowJobByID", mock.Anything, "itembase-app", "itembase", int64(42)).
		Return(&github.WorkflowJob{Status: github.Ptr("completed")}, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).Once()
	store.On("DeleteJob", mock.Anything, int64(42)).Return(nil).Once()

	removed := manager.reconcileStaleWorkflowJobs()
	if _, ok := removed[42]; !ok {
		t.Fatal("completed workflow job was not reconciled")
	}
	if len(manager.jobs) != 0 {
		t.Fatalf("completed workflow job remained in the local cache: %v", manager.jobs)
	}
}

func TestReconcileStaleWorkflowJobsRetainsJobOnAPIError(t *testing.T) {
	job := params.Job{
		WorkflowJobID: 43, Status: string(params.JobStatusQueued),
		RepositoryOwner: "itembase-app", RepositoryName: "itembase",
		CreatedAt: time.Now().Add(-11 * time.Minute),
	}
	manager, _, gh := staleTestManager(t, job)
	gh.On("GetWorkflowJobByID", mock.Anything, "itembase-app", "itembase", int64(43)).
		Return((*github.WorkflowJob)(nil), (*github.Response)(nil), errors.New("rate limited")).Once()

	removed := manager.reconcileStaleWorkflowJobs()
	if len(removed) != 0 {
		t.Fatalf("API failure must retain local row, removed=%v", removed)
	}
}

func TestReconcileStaleWorkflowJobsRetainsRemotelyQueuedJob(t *testing.T) {
	job := params.Job{
		WorkflowJobID: 45, Status: string(params.JobStatusQueued),
		RepositoryOwner: "itembase-app", RepositoryName: "itembase",
		CreatedAt: time.Now().Add(-11 * time.Minute),
	}
	manager, _, gh := staleTestManager(t, job)
	gh.On("GetWorkflowJobByID", mock.Anything, "itembase-app", "itembase", int64(45)).
		Return(&github.WorkflowJob{Status: github.Ptr("queued")}, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).Once()

	removed := manager.reconcileStaleWorkflowJobs()
	if len(removed) != 0 {
		t.Fatalf("live queued workflow job must be retained, removed=%v", removed)
	}
	if next := manager.staleJobChecks[45]; time.Until(next) <= 0 {
		t.Fatalf("expected a future reconciliation backoff, got %s", next)
	}
}

func TestReconcileStaleWorkflowJobsDeletesNotFoundJob(t *testing.T) {
	job := params.Job{
		WorkflowJobID: 44, Status: string(params.JobStatusQueued),
		RepositoryOwner: "itembase-app", RepositoryName: "itembase",
		CreatedAt: time.Now().Add(-11 * time.Minute),
	}
	manager, store, gh := staleTestManager(t, job)
	response := &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}
	gh.On("GetWorkflowJobByID", mock.Anything, "itembase-app", "itembase", int64(44)).
		Return((*github.WorkflowJob)(nil), response, errors.New("not found")).Once()
	store.On("DeleteJob", mock.Anything, int64(44)).Return(nil).Once()

	removed := manager.reconcileStaleWorkflowJobs()
	if _, ok := removed[44]; !ok {
		t.Fatal("404 workflow job was not reconciled")
	}
}

func TestUnlockExpiredJobClearsStaleInMemoryLock(t *testing.T) {
	store := dbMocks.NewStore(t)
	entityID := "b2f9b468-a313-4852-98c6-3b961471a072"
	manager := &basePoolManager{ctx: context.Background(), entity: params.ForgeEntity{ID: entityID}, store: store}
	job := params.Job{
		WorkflowJobID: 46,
		UpdatedAt:     time.Now().Add(-11 * time.Minute),
		LockedBy:      uuid.MustParse(entityID),
	}
	store.On("UnlockJob", mock.Anything, int64(46), entityID).Return(nil).Once()

	if err := manager.unlockExpiredJob(&job); err != nil {
		t.Fatalf("unlockExpiredJob returned error: %v", err)
	}
	if job.LockedBy != uuid.Nil {
		t.Fatalf("expected local job lock to be cleared, got %s", job.LockedBy)
	}
}
