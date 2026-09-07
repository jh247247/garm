package provider

import (
	"context"
	"errors"
	"testing"

	commonParams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/cache"
	dbMocks "github.com/cloudbase/garm/database/common/mocks"
	"github.com/cloudbase/garm/params"
	runnerMocks "github.com/cloudbase/garm/runner/common/mocks"
	"github.com/stretchr/testify/mock"
)

type publicationTokenGetter struct{}

func (publicationTokenGetter) NewInstanceJWTToken(params.Instance, params.ForgeEntity, uint) (string, error) {
	return "test-token", nil
}

func TestProviderPublicationCleanup(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		updateErr  error
		cancel     bool
		status     commonParams.InstanceStatus
		providerID string
		cleanupErr error
	}{
		{"published", nil, false, commonParams.InstanceRunning, "300", nil},
		{"database failure", errors.New("duplicated key not allowed"), false, commonParams.InstanceRunning, "300", nil},
		{"cancelled publication", context.Canceled, true, commonParams.InstanceRunning, "300", nil},
		{"provider error", nil, false, commonParams.InstanceError, "300", nil},
		{"empty ID publication failure", errors.New("publication failed"), false, commonParams.InstanceRunning, "", nil},
		{"cleanup failure preserves publication error", errors.New("publication failed"), false, commonParams.InstanceRunning, "300", errors.New("cleanup failed")},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := dbMocks.NewStore(t)
			provider := runnerMocks.NewProvider(t)
			entity := params.ForgeEntity{ID: "publication-test", EntityType: params.ForgeEntityTypeRepository}
			cache.SetGithubToolsCache(entity, nil)
			instance := params.Instance{Name: "garm-worker-test", ProviderID: "stale-id"}
			updated := params.Instance{Name: instance.Name, ProviderID: "300"}
			helper := &Provider{ctx: ctx, store: store, tokenGetter: publicationTokenGetter{}}
			manager := &instanceManager{ctx: ctx, instance: instance, provider: provider, helper: helper,
				scaleSet: params.ScaleSet{RepoID: entity.ID, TemplateID: 1}, scaleSetEntity: entity}
			store.On("GetForgeEntity", ctx, entity.EntityType, entity.ID).Return(entity, nil).Once()
			store.On("ControllerInfo").Return(params.ControllerInfo{}, nil).Once()
			provider.On("CreateInstance", ctx, mock.Anything, mock.Anything).
				Return(commonParams.ProviderInstance{ProviderID: scenario.providerID, Status: scenario.status}, nil).Once()
			store.On("UpdateInstance", ctx, instance.Name, mock.Anything).
				Run(func(mock.Arguments) {
					if scenario.cancel {
						cancel()
					}
				}).Return(updated, scenario.updateErr).Once()
			if scenario.updateErr != nil || scenario.status == commonParams.InstanceError {
				cleanupID := scenario.providerID
				if cleanupID == "" {
					cleanupID = instance.Name
				}
				provider.On("DeleteInstance", mock.MatchedBy(func(cleanup context.Context) bool {
					_, bounded := cleanup.Deadline()
					return cleanup.Err() == nil && bounded
				}), cleanupID, mock.Anything).Return(scenario.cleanupErr).Once()
			}
			if err := manager.handleCreateInstanceInProvider(instance); !errors.Is(err, scenario.updateErr) {
				t.Fatalf("publication error = %v, want %v", err, scenario.updateErr)
			}
			if scenario.updateErr == nil && manager.instance.ProviderID != "300" {
				t.Fatalf("published provider ID = %q", manager.instance.ProviderID)
			}
		})
	}
}
