package pool

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-github/v72/github"
	"github.com/stretchr/testify/mock"

	runnerErrors "github.com/cloudbase/garm-provider-common/errors"
	commonParams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/cache"
	dbMocks "github.com/cloudbase/garm/database/common/mocks"
	"github.com/cloudbase/garm/params"
	"github.com/cloudbase/garm/runner/common"
	runnerMocks "github.com/cloudbase/garm/runner/common/mocks"
)

const completedRunnerTestEntityID = "55c9c908-0321-4022-91b0-b9f4c7786c87"

func completedRunnerTestSetup(t *testing.T, instance params.Instance) (*basePoolManager, *dbMocks.Store, *runnerMocks.GithubClient, params.WorkflowJob) {
	t.Helper()

	entity := params.ForgeEntity{
		ID:         completedRunnerTestEntityID,
		EntityType: params.ForgeEntityTypeRepository,
		Owner:      "itembase-app",
		Name:       "itembase",
		Credentials: params.ForgeCredentials{
			ForgeType: params.GithubEndpointType,
		},
	}
	pool := params.Pool{
		ID:      "public-small",
		RepoID:  entity.ID,
		Enabled: true,
		Tags:    []params.Tag{{Name: "self-hosted"}, {Name: "public-small"}},
	}
	instance.PoolID = pool.ID
	instance.Status = commonParams.InstanceRunning

	cache.SetEntity(entity)
	cache.SetEntityPool(entity.ID, pool)
	cache.SetInstanceCache(instance)
	t.Cleanup(func() {
		cache.DeleteInstanceCache(instance.Name)
		cache.DeleteEntity(entity.ID)
	})

	store := dbMocks.NewStore(t)
	gh := runnerMocks.NewGithubClient(t)
	manager := &basePoolManager{
		ctx:              context.Background(),
		entity:           entity,
		store:            store,
		ghcli:            gh,
		managerIsRunning: true,
	}

	var event params.WorkflowJob
	event.Action = "completed"
	event.WorkflowJob.Labels = []string{"self-hosted", "public-small"}
	event.WorkflowJob.RunnerName = instance.Name
	event.Repository.Name = entity.Name
	event.Repository.Owner.Login = entity.Owner

	return manager, store, gh, event
}

func TestCompletedJITRunnerIsMarkedForDeletionWhenGithubAlreadyRemovedIt(t *testing.T) {
	instance := params.Instance{
		Name:             "completed-jit-runner",
		AgentID:          42,
		JitConfiguration: map[string]string{"encoded_jit_config": "redacted"},
	}
	manager, store, gh, event := completedRunnerTestSetup(t, instance)

	gh.On("RemoveEntityRunner", mock.Anything, instance.AgentID).
		Return(runnerErrors.ErrNotFound).Once()
	store.On("UpdateInstance", mock.Anything, instance.Name, mock.MatchedBy(func(update params.UpdateInstanceParams) bool {
		return update.Status == commonParams.InstancePendingDelete
	})).Return(instance, nil).Once()

	if err := manager.HandleWorkflowJob(event); err != nil {
		t.Fatalf("completed JIT runner should be retired after GitHub removes its registration: %v", err)
	}
}

func TestCompletedNonJITRunnerReturnsToIdle(t *testing.T) {
	instance := params.Instance{Name: "completed-reusable-runner", AgentID: 43}
	manager, store, _, event := completedRunnerTestSetup(t, instance)

	store.On("UpdateInstance", mock.Anything, instance.Name, mock.MatchedBy(func(update params.UpdateInstanceParams) bool {
		return update.RunnerStatus == params.RunnerIdle && update.Status == ""
	})).Return(instance, nil).Once()

	if err := manager.HandleWorkflowJob(event); err != nil {
		t.Fatalf("completed non-JIT runner should return to idle: %v", err)
	}
}

func TestCompletedJITRunnerDeletionFailsClosedOnUnauthorized(t *testing.T) {
	instance := params.Instance{
		Name:             "unauthorized-jit-runner",
		AgentID:          44,
		JitConfiguration: map[string]string{"encoded_jit_config": "redacted"},
	}
	manager, _, gh, event := completedRunnerTestSetup(t, instance)

	gh.On("RemoveEntityRunner", mock.Anything, instance.AgentID).
		Return(runnerErrors.ErrUnauthorized).Once()

	err := manager.HandleWorkflowJob(event)
	if !errors.Is(err, runnerErrors.ErrUnauthorized) {
		t.Fatalf("expected unauthorized runner deletion to fail closed, got %v", err)
	}
	if manager.Status().IsRunning {
		t.Fatal("unauthorized runner deletion must stop the pool manager")
	}
}

func TestCompletedJITRunnerDeletionRetainsInstanceOnGithubError(t *testing.T) {
	instance := params.Instance{
		Name:             "failed-jit-runner",
		AgentID:          45,
		JitConfiguration: map[string]string{"encoded_jit_config": "redacted"},
	}
	manager, _, gh, event := completedRunnerTestSetup(t, instance)

	githubErr := errors.New("github unavailable")
	gh.On("RemoveEntityRunner", mock.Anything, instance.AgentID).
		Return(githubErr).Once()

	err := manager.HandleWorkflowJob(event)
	if !errors.Is(err, githubErr) {
		t.Fatalf("expected GitHub removal error to preserve the runner for retry, got %v", err)
	}
	if !manager.Status().IsRunning {
		t.Fatal("transient GitHub errors must not permanently stop the pool manager")
	}
}

func TestAddRunnerHandlesJITCleanupAfterInstanceReservationFailure(t *testing.T) {
	tests := []struct {
		name              string
		cleanupErr        error
		expectPoolStopped bool
	}{
		{name: "cleanup succeeds"},
		{name: "registration already absent", cleanupErr: runnerErrors.ErrNotFound},
		{name: "github unavailable", cleanupErr: errors.New("github unavailable"), expectPoolStopped: true},
		{name: "github unauthorized", cleanupErr: runnerErrors.ErrUnauthorized, expectPoolStopped: true},
	}

	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entity := params.ForgeEntity{
				ID:         completedRunnerTestEntityID,
				EntityType: params.ForgeEntityTypeOrganization,
				Owner:      "itembase-app",
				Credentials: params.ForgeCredentials{
					ForgeType: params.GithubEndpointType,
				},
			}
			pool := params.Pool{
				ID:           "full-public-small",
				OrgID:        entity.ID,
				ProviderName: "proxmox-public",
				Enabled:      true,
				MaxRunners:   1,
				Tags:         []params.Tag{{Name: "self-hosted"}, {Name: "public-small"}},
			}
			store := dbMocks.NewStore(t)
			gh := runnerMocks.NewGithubClient(t)
			provider := runnerMocks.NewProvider(t)
			manager := &basePoolManager{
				ctx:              context.Background(),
				entity:           entity,
				store:            store,
				ghcli:            gh,
				providers:        map[string]common.Provider{pool.ProviderName: provider},
				managerIsRunning: true,
			}

			registration := &github.Runner{ID: github.Ptr(int64(46 + i))}
			jitConfig := map[string]string{"encoded_jit_config": "redacted"}
			reservationErr := errors.New("max runners reached")
			store.On("GetEntityPool", mock.Anything, entity, pool.ID).Return(pool, nil).Once()
			provider.On("DisableJITConfig").Return(false).Once()
			gh.On("GetEntityJITConfig", mock.Anything, mock.AnythingOfType("string"), pool, mock.Anything).
				Return(jitConfig, registration, nil).Once()
			store.On("CreateInstance", mock.Anything, pool.ID, mock.MatchedBy(func(create params.CreateInstanceParams) bool {
				return create.AgentID == registration.GetID() && len(create.JitConfiguration) > 0
			})).Return(params.Instance{}, reservationErr).Once()
			gh.On("RemoveEntityRunner", mock.Anything, registration.GetID()).Return(test.cleanupErr).Once()

			err := manager.AddRunner(context.Background(), pool.ID, nil)
			if !errors.Is(err, reservationErr) {
				t.Fatalf("expected capacity reservation error, got %v", err)
			}
			if test.cleanupErr != nil && !errors.Is(test.cleanupErr, runnerErrors.ErrNotFound) && !errors.Is(err, test.cleanupErr) {
				t.Fatalf("expected cleanup error to be returned, got %v", err)
			}
			expectedRunning := !test.expectPoolStopped
			if actualRunning := manager.Status().IsRunning; actualRunning != expectedRunning {
				t.Fatalf("pool manager running=%t, expected running=%t", actualRunning, expectedRunning)
			}
		})
	}
}
