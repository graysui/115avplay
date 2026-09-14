package models

import "time"

// Movie represents an item in offline_movies table.
type Movie struct {
	Code               string   `json:"code"`
	Title              string   `json:"title,omitempty"`
	OfficialTitle      string   `json:"official_title,omitempty"`
	Category           string   `json:"category"`
	PublishDate        *string  `json:"publish_date,omitempty"`
	ReleaseDate        *string  `json:"release_date,omitempty"`
	FirstSeenAt        string   `json:"first_seen_at"`
	PreviewImages      *string  `json:"preview_images,omitempty"`
	SourceWebsites     string   `json:"source_websites"` // JSON array
	TitleZh            *string  `json:"title_zh,omitempty"`
	DescriptionZh      *string  `json:"description_zh,omitempty"`
	CoverURL           *string  `json:"cover_url,omitempty"`
	PosterURL          *string  `json:"poster_url,omitempty"`
	Actors             string   `json:"actors"` // JSON array
	Tags               string   `json:"tags"`   // JSON array
	Maker              *string  `json:"maker,omitempty"`
	Director           *string  `json:"director,omitempty"`
	Score              *float64 `json:"score,omitempty"`
	RuntimeTicks       *int64   `json:"runtime_ticks,omitempty"`
	IsEnriched         int      `json:"is_enriched"` // 0, 1, 3
	ScrapePolicy       string   `json:"scrape_policy"` // auto, exempt, paused
	PolicyReason       *string  `json:"policy_reason,omitempty"`
	ScrapeStatus       string   `json:"scrape_status"` // idle, success, partial, not_found, transient
	ScrapeFailedReason *string  `json:"scrape_failed_reason,omitempty"`
	NotFoundCount      int      `json:"not_found_count"`
	LastNotFoundDay    *string  `json:"last_not_found_day,omitempty"`
	PartialAttempts    int      `json:"partial_attempts"`
	NextScrapeAt       *string  `json:"next_scrape_at,omitempty"`
	LastScrapedAt      *string  `json:"last_scraped_at,omitempty"`
	MetadataSources    string   `json:"metadata_sources"` // JSON object
	ManualFields       string   `json:"manual_fields"`    // JSON array
	LegacyMetadata     string   `json:"legacy_metadata"`  // JSON object
	DeletedAt          *string  `json:"deleted_at,omitempty"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
}

// Magnet represents an item in offline_magnets table.
type Magnet struct {
	InfoHash        string  `json:"info_hash"`
	MovieCode       string  `json:"movie_code"`
	ResourceKind    string  `json:"resource_kind"` // btih, ed2k, existing
	ContentHash     *string `json:"content_hash,omitempty"`
	MagnetURL       string  `json:"magnet_url"`
	Title           *string `json:"title,omitempty"`
	SizeBytes       int64   `json:"size_bytes"`
	Section         *string `json:"section,omitempty"`
	QualityLabel    string  `json:"quality_label"`
	Website         *string `json:"website,omitempty"`
	PublishDate     *string `json:"publish_date,omitempty"`
	HasChineseSub   int     `json:"has_chinese_sub"`
	IsCracked       int     `json:"is_cracked"`
	Is4K            int     `json:"is_4k"`
	IsCensored      int     `json:"is_censored"`
	PriorityScore   int     `json:"priority_score"` // Generated column in SQLite
	IsPreferred     int     `json:"is_preferred"`
	Enabled         int     `json:"enabled"`
	MetadataSources string  `json:"metadata_sources"` // JSON object
	ManualFields    string  `json:"manual_fields"`    // JSON array
	LegacyRuntime   string  `json:"legacy_runtime"`   // JSON object
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

// CloudBinding represents a 115 account connection.
type CloudBinding struct {
	ID               string `json:"id"`
	Provider         string `json:"provider"`
	ProviderUserID   string `json:"provider_user_id"`
	Enabled          int    `json:"enabled"`
	SecretSettingKey string `json:"secret_setting_key"`
	ConfigRevision   int    `json:"config_revision"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// CloudAsset represents a physical media asset in cloud storage.
type CloudAsset struct {
	ID           string  `json:"id"`
	BindingID    string  `json:"binding_id"`
	ResourceKey  *string `json:"resource_key,omitempty"`
	OwningJobID  *string `json:"owning_job_id,omitempty"`
	SourceType   string  `json:"source_type"` // permanent, temporary
	State        string  `json:"state"`       // ready, missing, pending_delete, deleting, deleted, quarantined
	Generation   int     `json:"generation"`
	FileID       *string `json:"file_id,omitempty"`
	PickCode     *string `json:"pick_code,omitempty"`
	FileName     *string `json:"file_name,omitempty"`
	SizeBytes    int64   `json:"size_bytes"`
	Container    *string `json:"container,omitempty"`
	RuntimeTicks *int64  `json:"runtime_ticks,omitempty"`
	MediaStreams *string `json:"media_streams,omitempty"`
	ParentID     *string `json:"parent_id,omitempty"`
	OwnedRootID  *string `json:"owned_root_id,omitempty"`
	RootSnapshot *string `json:"root_snapshot,omitempty"`
	ManifestJSON string  `json:"manifest_json"`
	ReadyAt      *string `json:"ready_at,omitempty"`
	ExpiresAt    *string `json:"expires_at,omitempty"`
	DeletedAt    *string `json:"deleted_at,omitempty"`
	LastSeenAt   *string `json:"last_seen_at,omitempty"`
	LastError    *string `json:"last_error,omitempty"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// Job represents an asynchronous persistent task.
type Job struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"` // auth, transfer, cleanup, scan, scrape, search, sync30d, import_full, rankings, delete_movie
	DedupeKey   string  `json:"dedupe_key"`
	ResourceKey *string `json:"resource_key,omitempty"`
	BindingID   *string `json:"binding_id,omitempty"`
	State       string  `json:"state"` // queued, running, reconcile, retry_wait, succeeded, failed, cancelled
	Generation  int     `json:"generation"`
	LeaseOwner  *string `json:"lease_owner,omitempty"`
	LeaseUntil  *string `json:"lease_until,omitempty"`
	Attempts    int     `json:"attempts"`
	NextRunAt   *string `json:"next_run_at,omitempty"`
	DeadlineAt  *string `json:"deadline_at,omitempty"`
	RemoteID    *string `json:"remote_id,omitempty"`
	OwnedRootID *string `json:"owned_root_id,omitempty"`
	ParamsJSON  string  `json:"params_json"`
	ResultJSON  string  `json:"result_json"`
	LastError   *string `json:"last_error,omitempty"`
	StartedAt   *string `json:"started_at,omitempty"`
	CompletedAt *string `json:"completed_at,omitempty"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// User represents a system user.
type User struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	PasswordHash       string `json:"-"`
	IsAdmin            int    `json:"is_admin"`
	Enabled            int    `json:"enabled"`
	MustChangePassword int    `json:"must_change_password"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
}

// AuthSession represents an authentication token session.
type AuthSession struct {
	ID         string  `json:"id"`
	UserID     string  `json:"user_id"`
	TokenHash  string  `json:"-"`
	Audience   string  `json:"audience"` // admin, emby
	DeviceID   string  `json:"device_id"`
	DeviceName *string `json:"device_name,omitempty"`
	ClientName *string `json:"client_name,omitempty"`
	CreatedAt  string  `json:"created_at"`
	ExpiresAt  string  `json:"expires_at"`
}

// PlaySession represents an active playback session.
type PlaySession struct {
	ID              string  `json:"id"`
	UserID          string  `json:"user_id"`
	MovieCode       string  `json:"movie_code"`
	ResourceKey     *string `json:"resource_key,omitempty"`
	AssetID         *string `json:"asset_id,omitempty"`
	DeviceID        string  `json:"device_id"`
	State           string  `json:"state"` // negotiating, active, stopped
	PositionTicks   int64   `json:"position_ticks"`
	DurationTicks   *int64  `json:"duration_ticks,omitempty"`
	LastSequence    *int    `json:"last_sequence,omitempty"`
	IsPaused        int     `json:"is_paused"`
	CountedComplete int     `json:"counted_complete"`
	LeaseUntil      *string `json:"lease_until,omitempty"`
	StartedAt       *string `json:"started_at,omitempty"`
	ClosedAt        *string `json:"closed_at,omitempty"`
	UpdatedAt       string  `json:"updated_at"`
}

// UserProgress tracks playback state per user and movie.
type UserProgress struct {
	UserID          string  `json:"user_id"`
	MovieCode       string  `json:"movie_code"`
	ActiveSessionID *string `json:"active_session_id,omitempty"`
	PositionTicks   int64   `json:"position_ticks"`
	DurationTicks   *int64  `json:"duration_ticks,omitempty"`
	Played          int     `json:"played"`
	Favorite        int     `json:"favorite"`
	PlayCount       int     `json:"play_count"`
	LastPlayedAt    *string `json:"last_played_at,omitempty"`
	UpdatedAt       string  `json:"updated_at"`
}

// Library represents a virtual media library view.
type Library struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Predicate string  `json:"predicate"`
	SortOrder int     `json:"sort_order"`
	Enabled   int     `json:"enabled"`
	CoverURL  *string `json:"cover_url,omitempty"`
}

// Schedule represents a periodic automated task schedule.
type Schedule struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"` // sync30d, rankings, scan
	Timezone   string  `json:"timezone"`
	RuleJSON   string  `json:"rule_json"`
	ParamsJSON string  `json:"params_json"`
	Enabled    int     `json:"enabled"`
	LastSlot   *string `json:"last_slot,omitempty"`
	NextRunAt  *string `json:"next_run_at,omitempty"`
	UpdatedAt  string  `json:"updated_at"`
}

// Alert represents an active or resolved system notification.
type Alert struct {
	Key              string  `json:"key"`
	State            string  `json:"state"` // active, resolved
	Message          string  `json:"message"`
	FirstSeenAt      string  `json:"first_seen_at"`
	LastSeenAt       string  `json:"last_seen_at"`
	LastDeliveredAt  *string `json:"last_delivered_at,omitempty"`
	NextDeliveryAt   *string `json:"next_delivery_at,omitempty"`
	DeliveryAttempts int     `json:"delivery_attempts"`
	DeliveryError    *string `json:"delivery_error,omitempty"`
}

// IngestAsset tracks a downloaded and processed release asset.
type IngestAsset struct {
	ID               string  `json:"id"`
	RunID            string  `json:"run_id"`
	ReleaseID        string  `json:"release_id"`
	Source           string  `json:"source"`
	AssetName        string  `json:"asset_name"`
	SHA256           string  `json:"sha256"`
	SizeBytes        int64   `json:"size_bytes"`
	CoverageStart    *string `json:"coverage_start,omitempty"`
	CoverageEnd      *string `json:"coverage_end,omitempty"`
	State            string  `json:"state"` // downloaded, processing, completed, failed
	LastCommittedRow int     `json:"last_committed_row"`
	CountsJSON       string  `json:"counts_json"`
	LastError        *string `json:"last_error,omitempty"`
	UpdatedAt        string  `json:"updated_at"`
}

// ScanRun tracks a 115 directory scan execution.
type ScanRun struct {
	ID          string  `json:"id"`
	JobID       string  `json:"job_id"`
	BindingID   string  `json:"binding_id"`
	RootID      string  `json:"root_id"`
	Mode        string  `json:"mode"` // full, incremental
	State       string  `json:"state"` // running, completed, failed
	StartedAt   string  `json:"started_at"`
	CompletedAt *string `json:"completed_at,omitempty"`
	LastError   *string `json:"last_error,omitempty"`
}

// ScanSeen tracks files observed during a scan.
type ScanSeen struct {
	ScanID      string  `json:"scan_id"`
	FileID      string  `json:"file_id"`
	ResourceKey *string `json:"resource_key,omitempty"`
}

// UTCNow returns the current UTC timestamp formatted in RFC3339 (seconds precision).
func UTCNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}
