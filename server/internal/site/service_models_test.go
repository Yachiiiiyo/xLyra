package site

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"xlyra/server/internal/store"
)

func TestListAllModelsBySiteGroupsFiltersAndSorts(t *testing.T) {
	t.Parallel()

	firstSiteID := uuid.New()
	secondSiteID := uuid.New()
	firstModel := store.SiteModel{ID: uuid.New(), SiteID: firstSiteID, UpstreamName: "gpt-a", Status: "active"}
	secondModel := store.SiteModel{ID: uuid.New(), SiteID: firstSiteID, UpstreamName: "gpt-b", Status: "disabled"}
	thirdModel := store.SiteModel{ID: uuid.New(), SiteID: secondSiteID, UpstreamName: "claude-a", Status: "active"}
	service := siteServiceWithQueryCallback(t, func(tx *gorm.DB) {
		models, ok := tx.Statement.Dest.(*[]store.SiteModel)
		if !ok {
			tx.AddError(gorm.ErrInvalidData)
			return
		}
		*models = []store.SiteModel{
			secondModel,
			{ID: uuid.New(), SiteID: firstSiteID, UpstreamName: "gpt-hidden", Status: "unavailable"},
			thirdModel,
			firstModel,
		}
		tx.RowsAffected = 4
	})

	got, err := service.ListAllModelsBySite(t.Context())
	if err != nil {
		t.Fatalf("ListAllModelsBySite() error = %v", err)
	}
	want := map[uuid.UUID][]store.SiteModel{
		firstSiteID:  {firstModel, secondModel},
		secondSiteID: {thirdModel},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListAllModelsBySite() = %#v, want %#v", got, want)
	}
}
