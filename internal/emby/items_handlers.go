package emby

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mediavault/internal/db"
	"mediavault/internal/models"
	"mediavault/internal/services"
)

type ItemsHandlers struct {
	serverID     string
	database     *db.DB
	movieRepo    *db.MovieRepo
	magnetRepo   *db.MagnetRepo
	assetRepo    *db.AssetRepo
	progressRepo *db.ProgressRepo
}

func NewItemsHandlers(
	serverID string,
	database *db.DB,
	movieRepo *db.MovieRepo,
	magnetRepo *db.MagnetRepo,
	assetRepo *db.AssetRepo,
	progressRepo *db.ProgressRepo,
) *ItemsHandlers {
	return &ItemsHandlers{
		serverID:     serverID,
		database:     database,
		movieRepo:    movieRepo,
		magnetRepo:   magnetRepo,
		assetRepo:    assetRepo,
		progressRepo: progressRepo,
	}
}

// GetItems handles GET /items and /users/{uid}/items.
func (h *ItemsHandlers) GetItems(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	// Verify user isolation if uid specified in query or path
	if reqUID := GetQueryParam(r, "userid"); reqUID != "" && reqUID != user.ID {
		http.Error(w, `{"error":"forbidden: cannot access another user's items"}`, http.StatusForbidden)
		return
	}

	parentID := strings.TrimSpace(GetQueryParam(r, "parentid"))
	searchTerm := strings.TrimSpace(GetQueryParam(r, "searchterm"))
	sortByRaw := strings.TrimSpace(GetQueryParam(r, "sortby"))
	sortOrderRaw := strings.TrimSpace(GetQueryParam(r, "sortorder"))

	startIndex := 0
	if s := GetQueryParam(r, "startindex"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= 0 {
			if v > 100000 {
				v = 100000
			}
			startIndex = v
		}
	}

	limit := 50
	if l := GetQueryParam(r, "limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			if v > 100 {
				v = 100
			}
			limit = v
		}
	}

	// Emby clients send a wide variety of SortBy values (and comma-separated lists).
	// Map every known field to a fixed, safe column expression; unknown values fall
	// back to the default instead of erroring (real Emby/Jellyfin ignore them too).
	sortColumn := "m.created_at"
	sortDirection := "DESC"
	if sortByRaw != "" {
		for _, raw := range strings.Split(sortByRaw, ",") {
			col, dir, ok := mapEmbySortField(strings.TrimSpace(raw))
			if ok {
				sortColumn = col
				sortDirection = dir
				break
			}
		}
	}
	if strings.EqualFold(sortOrderRaw, "ascending") {
		sortDirection = "ASC"
	} else if strings.EqualFold(sortOrderRaw, "descending") {
		sortDirection = "DESC"
	}

	var whereClauses []string
	var args []interface{}

	whereClauses = append(whereClauses, "m.deleted_at IS NULL")

	// Filter by parentID (library)
	if parentID != "" {
		if clause, ok := libraryPredicateClause(parentID); ok {
			whereClauses = append(whereClauses, clause)
		}
	}

	// SearchTerm
	if searchTerm != "" {
		pattern := "%" + searchTerm + "%"
		whereClauses = append(whereClauses, "(m.code LIKE ? OR m.title LIKE ? OR m.title_zh LIKE ?)")
		args = append(args, pattern, pattern, pattern)
	}

	whereSQL := strings.Join(whereClauses, " AND ")

	// Count total
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM offline_movies m WHERE %s", whereSQL)
	var totalCount int
	err := h.database.ExecRead(r.Context(), func(database *sql.DB) error {
		return database.QueryRowContext(r.Context(), countQuery, args...).Scan(&totalCount)
	})
	if err != nil {
		http.Error(w, `{"error":"failed to count items"}`, http.StatusInternalServerError)
		return
	}

	// Query items
	selectQuery := fmt.Sprintf(`
		SELECT m.code, m.title, m.official_title, m.category, m.publish_date, m.release_date,
		       m.first_seen_at, m.preview_images, m.title_zh, m.description_zh, m.cover_url,
		       m.poster_url, m.actors, m.tags, m.score, m.runtime_ticks, m.created_at,
		       p.position_ticks, p.duration_ticks, p.played, p.favorite, p.play_count, p.last_played_at
		FROM offline_movies m
		LEFT JOIN user_progress p ON p.movie_code = m.code AND p.user_id = ?
		WHERE %s
		ORDER BY %s %s, m.code ASC
		LIMIT ? OFFSET ?
	`, whereSQL, sortColumn, sortDirection)

	queryArgs := make([]interface{}, 0, len(args)+3)
	queryArgs = append(queryArgs, user.ID)
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, limit, startIndex)

	var items []ItemDTO
	err = h.database.ExecRead(r.Context(), func(database *sql.DB) error {
		rows, err := database.QueryContext(r.Context(), selectQuery, queryArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()

		prefix := PrefixFromContext(r.Context())
		for rows.Next() {
			item, err := scanItemDTO(rows, h.serverID, prefix)
			if err != nil {
				return err
			}
			items = append(items, *item)
		}
		return nil
	})

	if err != nil {
		http.Error(w, `{"error":"failed to query items"}`, http.StatusInternalServerError)
		return
	}

	if items == nil {
		items = []ItemDTO{}
	}

	writeJSON(w, http.StatusOK, ItemsResponse{
		Items:            items,
		TotalRecordCount: totalCount,
		StartIndex:       startIndex,
	})
}

// GetItemDetail handles GET /items/{id} and /users/{uid}/items/{id}.
func (h *ItemsHandlers) GetItemDetail(w http.ResponseWriter, r *http.Request, rawID string) {
	user := UserFromContext(r.Context())
	if user == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	movieCode, err := DecodeItemID(rawID)
	if err != nil {
		http.Error(w, `{"error":"invalid item id"}`, http.StatusNotFound)
		return
	}

	movie, err := h.movieRepo.GetMovie(r.Context(), movieCode)
	if err != nil || movie == nil || movie.DeletedAt != nil {
		http.Error(w, `{"error":"item not found"}`, http.StatusNotFound)
		return
	}

	// Fetch user progress
	var p models.UserProgress
	_ = h.database.ExecRead(r.Context(), func(database *sql.DB) error {
		return database.QueryRowContext(r.Context(), `
			SELECT position_ticks, duration_ticks, played, favorite, play_count, last_played_at
			FROM user_progress WHERE user_id = ? AND movie_code = ?
		`, user.ID, movieCode).Scan(&p.PositionTicks, &p.DurationTicks, &p.Played, &p.Favorite, &p.PlayCount, &p.LastPlayedAt)
	})

	prefix := PrefixFromContext(r.Context())
	item := buildFullItemDTO(movie, &p, h.serverID, prefix)

	// Fetch media sources
	magnets, _ := h.magnetRepo.ListMagnetsByMovie(r.Context(), movieCode)
	var sources []MediaSourceDTO
	now := models.UTCNow()
	nowTime := time.Now().UTC()

	binding, _ := h.assetRepo.GetActiveBinding(r.Context(), "115")
	var bindingID string
	if binding != nil {
		bindingID = binding.ID
	}

	for _, m := range magnets {
		if m.Enabled != 1 {
			continue
		}
		sourceID := EncodeSourceID(m.InfoHash)
		streamURL := fmt.Sprintf("%s/videos/%s/stream?MediaSourceId=%s", prefix, rawID, sourceID)

		// Check if asset is playable
		playable := false
		if bindingID != "" {
			asset, _ := h.assetRepo.GetReadyAssetByResource(r.Context(), m.InfoHash, bindingID, now)
			if asset != nil && services.IsAssetPlayable(asset, nowTime) {
				playable = true
			}
		}

		name := m.QualityLabel
		if m.HasChineseSub == 1 {
			name += " 中文字幕"
		}
		if m.Title != nil && *m.Title != "" {
			name += " - " + *m.Title
		}

		sources = append(sources, MediaSourceDTO{
			Id:                   sourceID,
			Name:                 name,
			Path:                 streamURL,
			DirectStreamUrl:      streamURL,
			Protocol:             "Http",
			Container:            "mp4",
			Size:                 m.SizeBytes,
			RunTimeTicks:         movie.RuntimeTicks,
			SupportsDirectPlay:   playable,
			SupportsDirectStream: false,
			SupportsTranscoding:  false,
			MediaStreams: []MediaStreamDTO{
				{
					Type:         "Video",
					DisplayTitle: m.QualityLabel,
					Codec:        "h264",
					Index:        0,
				},
			},
		})
	}

	item.MediaSources = sources
	writeJSON(w, http.StatusOK, item)
}

// GetCounts handles GET /items/counts.
func (h *ItemsHandlers) GetCounts(w http.ResponseWriter, r *http.Request) {
	var count int
	_ = h.database.ExecRead(r.Context(), func(database *sql.DB) error {
		return database.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM offline_movies WHERE deleted_at IS NULL`).Scan(&count)
	})

	writeJSON(w, http.StatusOK, CountsResponse{
		MovieCount:   count,
		SeriesCount:  0,
		EpisodeCount: 0,
	})
}

// GetNextUp handles GET /shows/nextup.
func (h *ItemsHandlers) GetNextUp(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, ItemsResponse{
		Items:            []ItemDTO{},
		TotalRecordCount: 0,
		StartIndex:       0,
	})
}

// GetResume handles GET /items/resume and /users/{uid}/items/resume.
func (h *ItemsHandlers) GetResume(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	limit := 20
	if l := GetQueryParam(r, "limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}

	resumes, err := h.progressRepo.ListResumeMovies(r.Context(), user.ID, limit)
	if err != nil {
		http.Error(w, `{"error":"failed to list resume items"}`, http.StatusInternalServerError)
		return
	}

	prefix := PrefixFromContext(r.Context())
	var items []ItemDTO
	for _, res := range resumes {
		dto := buildFullItemDTO(&res.Movie, &res.Progress, h.serverID, prefix)
		items = append(items, *dto)
	}

	writeJSON(w, http.StatusOK, ItemsResponse{
		Items:            items,
		TotalRecordCount: len(items),
		StartIndex:       0,
	})
}

// GetLatest handles GET /items/latest and /users/{uid}/items/latest.
// Returns an Item array directly (not wrapped in ItemsResponse).
// libraryPredicateClause maps a virtual library id to a SQL WHERE clause on the
// offline_movies alias "m". It is used by both the items list and the home-screen
// "Latest" rows so every library shows its own content.
func libraryPredicateClause(parentID string) (string, bool) {
	switch parentID {
	case "lib_chinese_sub":
		return "m.code IN (SELECT movie_code FROM offline_magnets WHERE has_chinese_sub = 1 AND enabled = 1)", true
	case "lib_censored":
		return "m.category = '亚洲有码'", true
	case "lib_uncensored":
		return "m.category = '亚洲无码'", true
	case "lib_4k":
		return "m.code IN (SELECT movie_code FROM offline_magnets WHERE is_4k = 1 AND enabled = 1)", true
	case "lib_fc2":
		return "m.category IN ('FC2','FC2/素人','素人')", true
	case "lib_domestic":
		return "m.category = '国产'", true
	case "lib_rank_weekly", "lib_rank_monthly", "lib_rank_top250":
		board := map[string]string{
			"lib_rank_weekly":  "weekly",
			"lib_rank_monthly": "monthly",
			"lib_rank_top250":  "top250",
		}[parentID]
		return fmt.Sprintf(
			"m.code IN (SELECT code FROM ranking_entries WHERE board = '%s') AND EXISTS (SELECT 1 FROM offline_magnets om WHERE om.movie_code = m.code AND om.enabled = 1)",
			board), true
	}
	return "", false
}

func (h *ItemsHandlers) GetLatest(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	limit := 20
	if l := GetQueryParam(r, "limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}

	parentID := strings.TrimSpace(GetQueryParam(r, "parentid"))
	where := "m.deleted_at IS NULL"
	if clause, ok := libraryPredicateClause(parentID); ok {
		where += " AND " + clause
	}

	query := `
		SELECT m.code, m.title, m.official_title, m.category, m.publish_date, m.release_date,
		       m.first_seen_at, m.preview_images, m.title_zh, m.description_zh, m.cover_url,
		       m.poster_url, m.actors, m.tags, m.score, m.runtime_ticks, m.created_at,
		       p.position_ticks, p.duration_ticks, p.played, p.favorite, p.play_count, p.last_played_at
		FROM offline_movies m
		LEFT JOIN user_progress p ON p.movie_code = m.code AND p.user_id = ?
		WHERE ` + where + `
		ORDER BY m.created_at DESC, m.code ASC
		LIMIT ?
	`

	var items []ItemDTO
	_ = h.database.ExecRead(r.Context(), func(database *sql.DB) error {
		rows, err := database.QueryContext(r.Context(), query, user.ID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()

		prefix := PrefixFromContext(r.Context())
		for rows.Next() {
			item, err := scanItemDTO(rows, h.serverID, prefix)
			if err != nil {
				return err
			}
			items = append(items, *item)
		}
		return nil
	})

	if items == nil {
		items = []ItemDTO{}
	}

	writeJSON(w, http.StatusOK, items)
}

// FavoriteItem handles POST /users/{uid}/favoriteitems/{id}.
func (h *ItemsHandlers) FavoriteItem(w http.ResponseWriter, r *http.Request, rawID string) {
	h.setFavorite(w, r, rawID, 1)
}

// UnfavoriteItem handles DELETE /users/{uid}/favoriteitems/{id}.
func (h *ItemsHandlers) UnfavoriteItem(w http.ResponseWriter, r *http.Request, rawID string) {
	h.setFavorite(w, r, rawID, 0)
}

func (h *ItemsHandlers) setFavorite(w http.ResponseWriter, r *http.Request, rawID string, favorite int) {
	user := UserFromContext(r.Context())
	movieCode, err := DecodeItemID(rawID)
	if err != nil {
		http.Error(w, `{"error":"invalid item id"}`, http.StatusBadRequest)
		return
	}

	now := models.UTCNow()
	err = h.database.ExecWrite(r.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(r.Context(), `
			INSERT INTO user_progress (user_id, movie_code, favorite, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(user_id, movie_code) DO UPDATE SET favorite = excluded.favorite, updated_at = excluded.updated_at
		`, user.ID, movieCode, favorite, now)
		return err
	})
	if err != nil {
		http.Error(w, `{"error":"failed to update favorite"}`, http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"IsFavorite": favorite == 1,
	})
}

// MarkPlayedItem handles POST /users/{uid}/playeditems/{id}.
func (h *ItemsHandlers) MarkPlayedItem(w http.ResponseWriter, r *http.Request, rawID string) {
	h.setPlayed(w, r, rawID, 1)
}

// UnmarkPlayedItem handles DELETE /users/{uid}/playeditems/{id}.
func (h *ItemsHandlers) UnmarkPlayedItem(w http.ResponseWriter, r *http.Request, rawID string) {
	h.setPlayed(w, r, rawID, 0)
}

func (h *ItemsHandlers) setPlayed(w http.ResponseWriter, r *http.Request, rawID string, played int) {
	user := UserFromContext(r.Context())
	movieCode, err := DecodeItemID(rawID)
	if err != nil {
		http.Error(w, `{"error":"invalid item id"}`, http.StatusBadRequest)
		return
	}

	now := models.UTCNow()
	err = h.database.ExecWrite(r.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(r.Context(), `
			INSERT INTO user_progress (user_id, movie_code, played, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(user_id, movie_code) DO UPDATE SET played = excluded.played, updated_at = excluded.updated_at
		`, user.ID, movieCode, played, now)
		return err
	})
	if err != nil {
		http.Error(w, `{"error":"failed to update played status"}`, http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"Played": played == 1,
	})
}

func scanItemDTO(rows *sql.Rows, serverID, prefix string) (*ItemDTO, error) {
	var code string
	var title, officialTitle, category, pubDate, relDate sql.NullString
	var firstSeen, previewImages, titleZh, descZh, coverURL, posterURL sql.NullString
	var actorsJSON, tagsJSON sql.NullString
	var score sql.NullFloat64
	var runtimeTicks sql.NullInt64
	var createdAt string
	var posTicks, durTicks sql.NullInt64
	var played, favorite, playCount sql.NullInt64
	var lastPlayedDate sql.NullString

	err := rows.Scan(
		&code, &title, &officialTitle, &category, &pubDate, &relDate,
		&firstSeen, &previewImages, &titleZh, &descZh, &coverURL,
		&posterURL, &actorsJSON, &tagsJSON, &score, &runtimeTicks, &createdAt,
		&posTicks, &durTicks, &played, &favorite, &playCount, &lastPlayedDate,
	)
	if err != nil {
		return nil, err
	}

	name := code
	if titleZh.Valid && titleZh.String != "" {
		name = titleZh.String
	} else if officialTitle.Valid && officialTitle.String != "" {
		name = officialTitle.String
	} else if title.Valid && title.String != "" {
		name = title.String
	}

	itemID := EncodeItemID(code)

	var year *int
	var premiereDate *string
	if relDate.Valid && len(relDate.String) >= 4 {
		if y, err := strconv.Atoi(relDate.String[:4]); err == nil {
			year = &y
		}
		premiereDate = &relDate.String
	} else if pubDate.Valid && len(pubDate.String) >= 4 {
		if y, err := strconv.Atoi(pubDate.String[:4]); err == nil {
			year = &y
		}
		premiereDate = &pubDate.String
	}

	var tags []string
	if tagsJSON.Valid && tagsJSON.String != "" {
		_ = json.Unmarshal([]byte(tagsJSON.String), &tags)
	}

	var actors []string
	if actorsJSON.Valid && actorsJSON.String != "" {
		_ = json.Unmarshal([]byte(actorsJSON.String), &actors)
	}
	people := make([]PersonDTO, len(actors))
	for i, a := range actors {
		people[i] = PersonDTO{Name: a, Id: fmt.Sprintf("actor_%d", i+1), Type: "Actor"}
	}

	var commRating *float64
	if score.Valid {
		r := score.Float64 * 2
		commRating = &r
	}

	var rTicks *int64
	if runtimeTicks.Valid {
		rTicks = &runtimeTicks.Int64
	}

	userData := &UserDataDTO{
		PlaybackPositionTicks: posTicks.Int64,
		Played:                played.Int64 == 1,
		IsFavorite:            favorite.Int64 == 1,
		PlayCount:             int(playCount.Int64),
	}
	if lastPlayedDate.Valid {
		userData.LastPlayedDate = &lastPlayedDate.String
	}

	overview := ""
	if descZh.Valid {
		overview = descZh.String
	}

	return &ItemDTO{
		Name:              name,
		ServerId:          serverID,
		Id:                itemID,
		Type:              "Movie",
		MediaType:         "Video",
		IsFolder:          false,
		SortName:          name,
		DateCreated:       createdAt,
		PremiereDate:      premiereDate,
		ProductionYear:    year,
		Overview:          overview,
		Genres:            tags,
		People:            people,
		CommunityRating:   commRating,
		RunTimeTicks:      rTicks,
		ImageTags:         &ImageTags{Primary: "img_" + itemID, Backdrop: "bd_" + itemID},
		BackdropImageTags: []string{"bd_" + itemID},
		UserData:          userData,
	}, nil
}

func buildFullItemDTO(m *models.Movie, p *models.UserProgress, serverID, prefix string) *ItemDTO {
	name := m.Code
	if m.TitleZh != nil && *m.TitleZh != "" {
		name = *m.TitleZh
	} else if m.OfficialTitle != "" {
		name = m.OfficialTitle
	} else if m.Title != "" {
		name = m.Title
	}

	itemID := EncodeItemID(m.Code)

	var year *int
	var premiereDate *string
	if m.ReleaseDate != nil && len(*m.ReleaseDate) >= 4 {
		if y, err := strconv.Atoi((*m.ReleaseDate)[:4]); err == nil {
			year = &y
		}
		premiereDate = m.ReleaseDate
	} else if m.PublishDate != nil && len(*m.PublishDate) >= 4 {
		if y, err := strconv.Atoi((*m.PublishDate)[:4]); err == nil {
			year = &y
		}
		premiereDate = m.PublishDate
	}

	var tags []string
	if m.Tags != "" {
		_ = json.Unmarshal([]byte(m.Tags), &tags)
	}

	var actors []string
	if m.Actors != "" {
		_ = json.Unmarshal([]byte(m.Actors), &actors)
	}
	people := make([]PersonDTO, len(actors))
	for i, a := range actors {
		people[i] = PersonDTO{Name: a, Id: fmt.Sprintf("actor_%d", i+1), Type: "Actor"}
	}

	var commRating *float64
	if m.Score != nil {
		r := *m.Score * 2
		commRating = &r
	}

	userData := &UserDataDTO{
		PlaybackPositionTicks: p.PositionTicks,
		Played:                p.Played == 1,
		IsFavorite:            p.Favorite == 1,
		PlayCount:             p.PlayCount,
		LastPlayedDate:        p.LastPlayedAt,
	}

	overview := ""
	if m.DescriptionZh != nil {
		overview = *m.DescriptionZh
	}

	origTitle := m.OfficialTitle

	return &ItemDTO{
		Name:              name,
		OriginalTitle:     origTitle,
		ServerId:          serverID,
		Id:                itemID,
		Type:              "Movie",
		MediaType:         "Video",
		IsFolder:          false,
		SortName:          name,
		DateCreated:       m.CreatedAt,
		PremiereDate:      premiereDate,
		ProductionYear:    year,
		Overview:          overview,
		Genres:            tags,
		People:            people,
		CommunityRating:   commRating,
		RunTimeTicks:      m.RuntimeTicks,
		ImageTags:         &ImageTags{Primary: "img_" + itemID, Backdrop: "bd_" + itemID},
		BackdropImageTags: []string{"bd_" + itemID},
		UserData:          userData,
	}
}

// mapEmbySortField maps an Emby/Jellyfin SortBy value to a fixed SQL column
// expression and default direction. The returned expression is never built from
// user input, so it is injection-safe. Unknown fields return ok=false so the
// caller can fall back to the default sort order (matching Emby behaviour).
func mapEmbySortField(field string) (column string, direction string, ok bool) {
	switch strings.ToLower(field) {
	case "datecreated", "dateadded", "datemodified", "startdate", "airtime":
		return "m.created_at", "DESC", true
	case "sortname", "sortnameordate", "seriessortname":
		return "COALESCE(m.title_zh, m.title)", "ASC", true
	case "premieredate", "productionyear":
		return "COALESCE(m.release_date, m.publish_date, m.first_seen_at)", "DESC", true
	case "communityrating", "criticrating", "officialrating":
		return "m.score", "DESC", true
	case "runtime":
		return "m.runtime_ticks", "DESC", true
	case "random":
		return "RANDOM()", "ASC", true
	case "dateplayed", "playcount", "isplayed":
		return "m.created_at", "DESC", true
	default:
		return "", "", false
	}
}
