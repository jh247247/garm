package pool

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudbase/garm/cache"
	dbMocks "github.com/cloudbase/garm/database/common/mocks"
	"github.com/cloudbase/garm/params"
)

func TestCurrentEntityPoolsRefreshesExternalLimitChanges(t *testing.T) {
	store := dbMocks.NewStore(t)
	entity := params.ForgeEntity{ID: "capacity-refresh-repo", EntityType: params.ForgeEntityTypeRepository}
	stale := params.Pool{ID: "large", RepoID: entity.ID, Enabled: true, MaxRunners: 1, MinIdleRunners: 1}
	fresh := params.Pool{ID: "large", RepoID: entity.ID, Enabled: true, MaxRunners: 1, MinIdleRunners: 0}

	cache.SetEntity(entity)
	cache.ReplaceEntityPools(entity.ID, []params.Pool{stale})
	t.Cleanup(func() { cache.DeleteEntity(entity.ID) })
	store.On("ListEntityPools", context.Background(), entity).Return([]params.Pool{fresh}, nil).Once()

	manager := &basePoolManager{ctx: context.Background(), entity: entity, store: store}
	pools, err := manager.currentEntityPools()
	if err != nil {
		t.Fatalf("currentEntityPools returned error: %v", err)
	}
	if len(pools) != 1 || pools[0].MinIdleRunners != 0 {
		t.Fatalf("expected fresh pool limits, got %#v", pools)
	}
	cached, ok := cache.GetEntityPool(entity.ID, fresh.ID)
	if !ok || cached.MinIdleRunners != 0 {
		t.Fatalf("expected refreshed cache, got %#v, found=%v", cached, ok)
	}
}

func TestCurrentEntityPoolsPreservesCacheOnReadFailure(t *testing.T) {
	store := dbMocks.NewStore(t)
	entity := params.ForgeEntity{ID: "capacity-refresh-error-repo", EntityType: params.ForgeEntityTypeRepository}
	stale := params.Pool{ID: "medium", RepoID: entity.ID, Enabled: true, MaxRunners: 1}

	cache.SetEntity(entity)
	cache.ReplaceEntityPools(entity.ID, []params.Pool{stale})
	t.Cleanup(func() { cache.DeleteEntity(entity.ID) })
	store.On("ListEntityPools", context.Background(), entity).Return(nil, errors.New("database busy")).Once()

	manager := &basePoolManager{ctx: context.Background(), entity: entity, store: store}
	if _, err := manager.currentEntityPools(); err == nil {
		t.Fatal("expected currentEntityPools to fail")
	}
	cached, ok := cache.GetEntityPool(entity.ID, stale.ID)
	if !ok || cached.MaxRunners != stale.MaxRunners {
		t.Fatalf("failed refresh must preserve the existing cache, got %#v, found=%v", cached, ok)
	}
}
