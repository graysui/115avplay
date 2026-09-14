package emby

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"
)

// PublicSystemInfo is returned by /system/info/public.
type PublicSystemInfo struct {
	ServerName             string `json:"ServerName"`
	Version                string `json:"Version"`
	Id                     string `json:"Id"`
	OperatingSystem        string `json:"OperatingSystem"`
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
}

// SystemInfo is returned by /system/info.
type SystemInfo struct {
	ServerName             string   `json:"ServerName"`
	Version                string   `json:"Version"`
	Id                     string   `json:"Id"`
	InternalAddress        string   `json:"InternalAddress"`
	LocalAddress           string   `json:"LocalAddress"`
	OperatingSystem        string   `json:"OperatingSystem"`
	CanSelfRestart         bool     `json:"CanSelfRestart"`
	WebSocketPortNumber    int      `json:"WebSocketPortNumber"`
	CompletedInstallations []string `json:"CompletedInstallations"`
	StartupWizardCompleted bool     `json:"StartupWizardCompleted"`
}

// UserPolicy represents user access permissions in Emby.
type UserPolicy struct {
	IsAdministrator                  bool `json:"IsAdministrator"`
	IsHidden                         bool `json:"IsHidden"`
	IsDisabled                       bool `json:"IsDisabled"`
	EnableSharedDevice               bool `json:"EnableSharedDevice"`
	EnableRemoteControlOfOtherUsers  bool `json:"EnableRemoteControlOfOtherUsers"`
	EnableLiveTvManagement           bool `json:"EnableLiveTvManagement"`
	EnableLiveTvAccess               bool `json:"EnableLiveTvAccess"`
	EnableMediaPlayback              bool `json:"EnableMediaPlayback"`
	EnableAudioPlaybackTranscoding   bool `json:"EnableAudioPlaybackTranscoding"`
	EnableVideoPlaybackTranscoding   bool `json:"EnableVideoPlaybackTranscoding"`
	EnablePlaybackRemuxing           bool `json:"EnablePlaybackRemuxing"`
	ForceRemoteSourceTranscoding     bool `json:"ForceRemoteSourceTranscoding"`
	EnableContentDeleting            bool `json:"EnableContentDeleting"`
	EnableContentDownloading         bool `json:"EnableContentDownloading"`
	EnableSyncTranscoding            bool `json:"EnableSyncTranscoding"`
}

// UserConfiguration represents Emby user configuration.
type UserConfiguration struct {
	AudioLanguagePreference    string   `json:"AudioLanguagePreference,omitempty"`
	PlayDefaultAudioTrack      bool     `json:"PlayDefaultAudioTrack"`
	SubtitleLanguagePreference string   `json:"SubtitleLanguagePreference,omitempty"`
	DisplayMissingEpisodes     bool     `json:"DisplayMissingEpisodes"`
	GroupedFolders             []string `json:"GroupedFolders"`
	SubtitleMode               string   `json:"SubtitleMode"`
	DisplayCollectionsView     bool     `json:"DisplayCollectionsView"`
	EnableLocalPassword        bool     `json:"EnableLocalPassword"`
	OrderedViews               []string `json:"OrderedViews"`
	LatestItemsExcludes        []string `json:"LatestItemsExcludes"`
	MyMediaExcludes            []string `json:"MyMediaExcludes"`
	HidePlayedInLatest         bool     `json:"HidePlayedInLatest"`
	RememberAudioSelections    bool     `json:"RememberAudioSelections"`
	RememberSubtitleSelections bool     `json:"RememberSubtitleSelections"`
	EnableNextEpisodeAutoPlay  bool     `json:"EnableNextEpisodeAutoPlay"`
}

// UserDTO represents an Emby user.
type UserDTO struct {
	Name                      string             `json:"Name"`
	ServerId                  string             `json:"ServerId"`
	Id                        string             `json:"Id"`
	HasPassword               bool               `json:"HasPassword"`
	HasConfiguredPassword     bool               `json:"HasConfiguredPassword"`
	HasConfiguredEasyPassword bool               `json:"HasConfiguredEasyPassword"`
	EnableAutoLogin           bool               `json:"EnableAutoLogin"`
	LastLoginDate             string             `json:"LastLoginDate,omitempty"`
	LastActivityDate          string             `json:"LastActivityDate,omitempty"`
	Configuration             *UserConfiguration `json:"Configuration,omitempty"`
	Policy                    UserPolicy         `json:"Policy"`
}

// AuthSessionInfo represents session info in login response.
type AuthSessionInfo struct {
	Id                 string `json:"Id"`
	UserId             string `json:"UserId"`
	UserName           string `json:"UserName"`
	DeviceId           string `json:"DeviceId,omitempty"`
	DeviceName         string `json:"DeviceName,omitempty"`
	Client             string `json:"Client,omitempty"`
	ApplicationVersion string `json:"ApplicationVersion,omitempty"`
}

// AuthResponse is returned by /users/authenticatebyname.
type AuthResponse struct {
	User        UserDTO         `json:"User"`
	SessionInfo AuthSessionInfo `json:"SessionInfo"`
	AccessToken string          `json:"AccessToken"`
	ServerId    string          `json:"ServerId"`
}

// UserDataDTO represents user-specific playback information for an item.
type UserDataDTO struct {
	PlaybackPositionTicks int64   `json:"PlaybackPositionTicks"`
	PlayCount             int     `json:"PlayCount"`
	IsFavorite            bool    `json:"IsFavorite"`
	Played                bool    `json:"Played"`
	Key                   string  `json:"Key,omitempty"`
	LastPlayedDate        *string `json:"LastPlayedDate,omitempty"`
}

// ImageTags represents available image identifiers.
type ImageTags struct {
	Primary  string `json:"Primary,omitempty"`
	Backdrop string `json:"Backdrop,omitempty"`
}

// PersonDTO represents an actor/director in Emby.
type PersonDTO struct {
	Name string `json:"Name"`
	Id   string `json:"Id"`
	Role string `json:"Role,omitempty"`
	Type string `json:"Type"`
}

// MediaStreamDTO represents audio/video/subtitle stream metadata.
type MediaStreamDTO struct {
	Codec                  string  `json:"Codec,omitempty"`
	Language               string  `json:"Language,omitempty"`
	TimeBase               string  `json:"TimeBase,omitempty"`
	CodecTimeBase          string  `json:"CodecTimeBase,omitempty"`
	VideoRange             string  `json:"VideoRange,omitempty"`
	DisplayTitle           string  `json:"DisplayTitle,omitempty"`
	IsInterlaced           bool    `json:"IsInterlaced"`
	BitRate                int64   `json:"BitRate,omitempty"`
	BitDepth               int     `json:"BitDepth,omitempty"`
	RefFrames              int     `json:"RefFrames,omitempty"`
	IsDefault              bool    `json:"IsDefault"`
	IsForced               bool    `json:"IsForced"`
	Height                 int     `json:"Height,omitempty"`
	Width                  int     `json:"Width,omitempty"`
	AverageFrameRate       float64 `json:"AverageFrameRate,omitempty"`
	RealFrameRate          float64 `json:"RealFrameRate,omitempty"`
	Profile                string  `json:"Profile,omitempty"`
	Type                   string  `json:"Type"` // Video, Audio, Subtitle
	AspectRatio            string  `json:"AspectRatio,omitempty"`
	Index                  int     `json:"Index"`
	Score                  int     `json:"Score,omitempty"`
	IsExternal             bool    `json:"IsExternal"`
	DeliveryMethod         string  `json:"DeliveryMethod,omitempty"`
	DeliveryURL            string  `json:"DeliveryUrl,omitempty"`
	IsExternalURL          bool    `json:"IsExternalUrl,omitempty"`
	SupportsExternalStream bool    `json:"SupportsExternalStream,omitempty"`
	Path                   string  `json:"Path,omitempty"`
	Protocol               string  `json:"Protocol,omitempty"`
	PixelFormat            string  `json:"PixelFormat,omitempty"`
	Level                  int     `json:"Level,omitempty"`
	IsAnamorphic           bool    `json:"IsAnamorphic,omitempty"`
	Channels               int     `json:"Channels,omitempty"`
	SampleRate             int     `json:"SampleRate,omitempty"`
	Title                  string  `json:"Title,omitempty"`
}

// MediaSourceDTO represents a playable source version.
type MediaSourceDTO struct {
	Id                          string           `json:"Id"`
	Name                        string           `json:"Name"`
	Path                        string           `json:"Path,omitempty"`
	DirectStreamUrl             string           `json:"DirectStreamUrl,omitempty"`
	Protocol                    string           `json:"Protocol"`
	MediaStreams                []MediaStreamDTO `json:"MediaStreams"`
	Bitrate                     int64            `json:"Bitrate,omitempty"`
	Container                   string           `json:"Container,omitempty"`
	Size                        int64            `json:"Size,omitempty"`
	RunTimeTicks                *int64           `json:"RunTimeTicks,omitempty"`
	SupportsDirectPlay          bool             `json:"SupportsDirectPlay"`
	SupportsDirectStream        bool             `json:"SupportsDirectStream"`
	SupportsTranscoding         bool             `json:"SupportsTranscoding"`
	IsInfiniteStream            bool             `json:"IsInfiniteStream"`
	RequiresOpening             bool             `json:"RequiresOpening"`
	RequiresClosing             bool             `json:"RequiresClosing"`
	RequiresLooping             bool             `json:"RequiresLooping"`
	SupportsProbing             bool             `json:"SupportsProbing"`
	VideoType                   string           `json:"VideoType,omitempty"`
	IsoType                     string           `json:"IsoType,omitempty"`
	Type                        string           `json:"Type"`
	DefaultAudioStreamIndex     *int             `json:"DefaultAudioStreamIndex,omitempty"`
	DefaultSubtitleStreamIndex  *int             `json:"DefaultSubtitleStreamIndex,omitempty"`
}

// ItemDTO represents a movie or collection folder item.
type ItemDTO struct {
	Name                  string           `json:"Name"`
	ServerId              string           `json:"ServerId"`
	Id                    string           `json:"Id"`
	RunTimeTicks          *int64           `json:"RunTimeTicks,omitempty"`
	ProductionYear        *int             `json:"ProductionYear,omitempty"`
	PremiereDate          *string          `json:"PremiereDate,omitempty"`
	DateCreated           string           `json:"DateCreated,omitempty"`
	SortName              string           `json:"SortName,omitempty"`
	OfficialRating        string           `json:"OfficialRating,omitempty"`
	Overview              string           `json:"Overview,omitempty"`
	Genres                []string         `json:"Genres,omitempty"`
	CommunityRating       *float64         `json:"CommunityRating,omitempty"`
	IsFolder              bool             `json:"IsFolder"`
	Type                  string           `json:"Type"` // Movie, CollectionFolder, UserRootFolder
	CollectionType        string           `json:"CollectionType,omitempty"` // movies
	ImageTags             *ImageTags       `json:"ImageTags,omitempty"`
	BackdropImageTags     []string         `json:"BackdropImageTags,omitempty"`
	People                []PersonDTO      `json:"People,omitempty"`
	Studios               []string         `json:"Studios,omitempty"`
	UserData              *UserDataDTO     `json:"UserData,omitempty"`
	MediaSources          []MediaSourceDTO `json:"MediaSources,omitempty"`
	OriginalTitle         string           `json:"OriginalTitle,omitempty"`
	CanDelete             bool             `json:"CanDelete"`
	CanDownload           bool             `json:"CanDownload"`
	SupportsSync          bool             `json:"SupportsSync"`
	Container             string           `json:"Container,omitempty"`
	MediaType             string           `json:"MediaType,omitempty"` // Video
	Width                 *int             `json:"Width,omitempty"`
	Height                *int             `json:"Height,omitempty"`
}

// ItemsResponse is returned by /items and /users/{uid}/items.
type ItemsResponse struct {
	Items            []ItemDTO `json:"Items"`
	TotalRecordCount int       `json:"TotalRecordCount"`
	StartIndex       int       `json:"StartIndex"`
}

// PlaybackInfoResponse is returned by /items/{id}/playbackinfo.
type PlaybackInfoResponse struct {
	MediaSources  []MediaSourceDTO `json:"MediaSources"`
	PlaySessionId string           `json:"PlaySessionId"`
}

// CountsResponse is returned by /items/counts.
type CountsResponse struct {
	MovieCount   int `json:"MovieCount"`
	SeriesCount  int `json:"SeriesCount"`
	EpisodeCount int `json:"EpisodeCount"`
}

// ID Encoding / Decoding functions according to design:
// ItemId = "mov_" + base64url(code UTF-8)
// MediaSourceId = "src_" + base64url(resource_key UTF-8)

// EncodeItemID encodes a movie code into an Emby ItemId.
func EncodeItemID(movieCode string) string {
	return "mov_" + base64.RawURLEncoding.EncodeToString([]byte(movieCode))
}

// DecodeItemID decodes an Emby ItemId back to a movie code.
func DecodeItemID(itemID string) (string, error) {
	if !strings.HasPrefix(itemID, "mov_") {
		return "", fmt.Errorf("invalid item id format: must have mov_ prefix")
	}
	encoded := strings.TrimPrefix(itemID, "mov_")
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("invalid base64url item id: %w", err)
	}
	code := string(data)
	if !utf8.ValidString(code) || len(code) == 0 {
		return "", fmt.Errorf("invalid utf-8 movie code")
	}
	return code, nil
}

// EncodeSourceID encodes a magnet/asset resource key into an Emby MediaSourceId.
func EncodeSourceID(resourceKey string) string {
	return "src_" + base64.RawURLEncoding.EncodeToString([]byte(resourceKey))
}

// DecodeSourceID decodes an Emby MediaSourceId back to a resource key.
func DecodeSourceID(sourceID string) (string, error) {
	if !strings.HasPrefix(sourceID, "src_") {
		return "", fmt.Errorf("invalid source id format: must have src_ prefix")
	}
	encoded := strings.TrimPrefix(sourceID, "src_")
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("invalid base64url source id: %w", err)
	}
	key := string(data)
	if !utf8.ValidString(key) || len(key) == 0 {
		return "", fmt.Errorf("invalid utf-8 resource key")
	}
	return key, nil
}
