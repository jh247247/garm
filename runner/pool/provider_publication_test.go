package pool

import (
	"context"
	"errors"
	"testing"

	commonParams "github.com/cloudbase/garm-provider-common/params"
	dbMocks "github.com/cloudbase/garm/database/common/mocks"
	"github.com/cloudbase/garm/params"
	"github.com/cloudbase/garm/runner/common"
	runnerMocks "github.com/cloudbase/garm/runner/common/mocks"
	"github.com/stretchr/testify/mock"
)

type publicationTokenGetter struct{}

func (publicationTokenGetter) NewInstanceJWTToken(params.Instance, params.ForgeEntity, uint) (string, error) {
	return "test-token", nil
}

func TestProviderPublicationCleanup(t *testing.T) {
	for _, test := range []struct {
		name      string
		updateErr error
		cancel    bool
	}{
		{"published", nil, false},
		{"database failure", errors.New("duplicated key not allowed"), false},
		{"cancelled publication", context.Canceled, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := dbMocks.NewStore(t)
			provider := runnerMocks.NewProvider(t)
			entity := params.ForgeEntity{ID: "repo", EntityType: params.ForgeEntityTypeRepository}
			pool := params.Pool{ID: "pool", ProviderName: "proxmox", TemplateID: 1}
			instance := params.Instance{Name: "garm-test", PoolID: pool.ID, JitConfiguration: map[string]string{"encoded_jit_config": "test"}}
			manager := &basePoolManager{ctx: ctx, entity: entity, store: store,
				instanceTokenGetter: publicationTokenGetter{}, providers: map[string]common.Provider{"proxmox": provider}}
			store.On("GetEntityPool", ctx, entity, pool.ID).Return(pool, nil).Once()
			provider.On("CreateInstance", ctx, mock.Anything, mock.Anything).
				Return(commonParams.ProviderInstance{ProviderID: "300", Status: commonParams.InstanceRunning}, nil).Once()
			store.On("UpdateInstance", ctx, instance.Name, mock.Anything).
				Run(func(mock.Arguments) {
					if test.cancel {
						cancel()
					}
				}).Return(instance, test.updateErr).Once()
			if test.updateErr != nil {
				provider.On("DeleteInstance", mock.MatchedBy(func(cleanup context.Context) bool {
					_, bounded := cleanup.Deadline()
					return cleanup.Err() == nil && bounded
				}), "300", mock.Anything).Return(nil).Once()
			}
			if err := manager.addInstanceToProvider(instance); !errors.Is(err, test.updateErr) {
				t.Fatalf("publication error = %v, want %v", err, test.updateErr)
			}
		})
	}
}
