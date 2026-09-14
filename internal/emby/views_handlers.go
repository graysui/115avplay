package emby

import (
	"net/http"

	"mediavault/internal/db"
)

type ViewsHandlers struct {
	serverID    string
	libraryRepo *db.LibraryRepo
}

func NewViewsHandlers(serverID string, libraryRepo *db.LibraryRepo) *ViewsHandlers {
	return &ViewsHandlers{
		serverID:    serverID,
		libraryRepo: libraryRepo,
	}
}

// GetViews handles GET /users/{uid}/views and /library/mediafolders.
func (h *ViewsHandlers) GetViews(w http.ResponseWriter, r *http.Request) {
	libs, err := h.libraryRepo.ListLibraries(r.Context())
	if err != nil {
		http.Error(w, `{"error":"failed to list libraries"}`, http.StatusInternalServerError)
		return
	}

	var items []ItemDTO
	for _, l := range libs {
		if l.Enabled != 1 {
			continue
		}
		items = append(items, ItemDTO{
			Name:           l.Name,
			ServerId:       h.serverID,
			Id:             l.ID,
			Type:           "CollectionFolder",
			CollectionType: "movies",
			IsFolder:       true,
		})
	}

	writeJSON(w, http.StatusOK, ItemsResponse{
		Items:            items,
		TotalRecordCount: len(items),
		StartIndex:       0,
	})
}
