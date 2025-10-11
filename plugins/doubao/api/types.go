// Package api has type definitions for pikpak
//
// Manually obtained from the API responses using Browse Dev. Tool and https://mholt.github.io/json-to-go/
package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/rclone/rclone/lib/rest"
)

const (
	// "2022-09-17T14:31:06.056+08:00"
	timeFormat = `"` + time.RFC3339 + `"`
)

// Time represents date and time information for the pikpak API, by using RFC3339
type Time time.Time

// MarshalJSON turns a Time into JSON (in UTC)
func (t *Time) MarshalJSON() (out []byte, err error) {
	timeString := (*time.Time)(t).Format(timeFormat)
	return []byte(timeString), nil
}

// UnmarshalJSON turns JSON into a Time
func (t *Time) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || string(data) == `""` {
		return nil
	}
	newT, err := time.Parse(timeFormat, string(data))
	if err != nil {
		return err
	}
	*t = Time(newT)
	return nil
}

// Types of things in Item
const (
	KindOfFolder        = "drive#folder"
	KindOfFile          = "drive#file"
	KindOfFileList      = "drive#fileList"
	KindOfResumable     = "drive#resumable"
	KindOfForm          = "drive#form"
	ThumbnailSizeS      = "SIZE_SMALL"
	ThumbnailSizeM      = "SIZE_MEDIUM"
	ThumbnailSizeL      = "SIZE_LARGE"
	PhaseTypeComplete   = "PHASE_TYPE_COMPLETE"
	PhaseTypeRunning    = "PHASE_TYPE_RUNNING"
	PhaseTypeError      = "PHASE_TYPE_ERROR"
	PhaseTypePending    = "PHASE_TYPE_PENDING"
	UploadTypeForm      = "UPLOAD_TYPE_FORM"
	UploadTypeResumable = "UPLOAD_TYPE_RESUMABLE"
	ListLimit           = 500
)

// ------------------------------------------------------------

// Error details api error from pikpak
type Error struct {
	Reason  string `json:"error"` // short description of the reason, e.g. "file_name_empty" "invalid_request"
	Code    int    `json:"error_code"`
	URL     string `json:"error_url,omitempty"`
	Message string `json:"error_description,omitempty"`
	// can have either of `error_details` or `details``
	ErrorDetails []*ErrorDetails `json:"error_details,omitempty"`
	Details      []*ErrorDetails `json:"details,omitempty"`
}

// ErrorDetails contains further details of api error
type ErrorDetails struct {
	Type         string   `json:"@type,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Domain       string   `json:"domain,omitempty"`
	Metadata     struct{} `json:"metadata,omitempty"` // TODO: undiscovered yet
	Locale       string   `json:"locale,omitempty"`   // e.g. "en"
	Message      string   `json:"message,omitempty"`
	StackEntries []any    `json:"stack_entries,omitempty"` // TODO: undiscovered yet
	Detail       string   `json:"detail,omitempty"`
}

// Error returns a string for the error and satisfies the error interface
func (e *Error) Error() string {
	out := fmt.Sprintf("Error %q (%d)", e.Reason, e.Code)
	if e.Message != "" {
		out += ": " + e.Message
	}
	return out
}

// Check Error satisfies the error interface
var _ error = (*Error)(nil)

type ReqCallback func(req *rest.Opts)

// ------------------------------------------------------------

// ------------------------------------------------------------
// Common Elements

// Link contains a download URL for opening files
type Link struct {
	URL    string `json:"url"`
	Expire Time   `json:"expire"`
}

// Valid reports whether l is non-nil, has an URL, and is not expired.
// It primarily checks the URL's expire query parameter, falling back to the Expire field.
func (l *Link) Valid() bool {
	if l == nil || l.URL == "" {
		return false
	}
	// Primary validation: check URL's expire query parameter
	if u, err := url.Parse(l.URL); err == nil {
		if expireStr := u.Query().Get("expireTime"); expireStr != "" {
			// Try parsing as Unix timestamp (seconds)
			if expireInt, err := strconv.ParseInt(expireStr, 10, 64); err == nil {
				expireTime := time.Unix(expireInt, 0)
				return time.Now().Add(10 * time.Second).Before(expireTime)
			}
		}
	}

	// Fallback validation: use the Expire field if URL parsing didn't work
	return time.Now().Add(10 * time.Second).Before(time.Time(l.Expire))
}

// URL is a basic form of URL
type URL struct {
	Kind string `json:"kind,omitempty"` // e.g. "upload#url"
	URL  string `json:"url,omitempty"`
}

// ------------------------------------------------------------
// Base Elements

// Task is a basic element representing a single task such as offline download and upload
type Task struct {
	Kind              string      `json:"kind,omitempty"` // "drive#task"
	ID                string      `json:"id,omitempty"`   // task id?
	Name              string      `json:"name,omitempty"` // torrent name?
	Type              string      `json:"type,omitempty"` // "offline"
	UserID            string      `json:"user_id,omitempty"`
	Statuses          []any       `json:"statuses,omitempty"`    // TODO
	StatusSize        int         `json:"status_size,omitempty"` // TODO
	Params            *TaskParams `json:"params,omitempty"`      // TODO
	FileID            string      `json:"file_id,omitempty"`
	FileName          string      `json:"file_name,omitempty"`
	FileSize          string      `json:"file_size,omitempty"`
	Message           string      `json:"message,omitempty"` // e.g. "Saving"
	CreatedTime       Time        `json:"created_time,omitempty"`
	UpdatedTime       Time        `json:"updated_time,omitempty"`
	ThirdTaskID       string      `json:"third_task_id,omitempty"` // TODO
	Phase             string      `json:"phase,omitempty"`         // e.g. "PHASE_TYPE_RUNNING"
	Progress          int         `json:"progress,omitempty"`
	IconLink          string      `json:"icon_link,omitempty"`
	Callback          string      `json:"callback,omitempty"`
	ReferenceResource any         `json:"reference_resource,omitempty"` // TODO
	Space             string      `json:"space,omitempty"`
}

// TaskParams includes parameters informing status of Task
type TaskParams struct {
	Age          string `json:"age,omitempty"`
	PredictSpeed string `json:"predict_speed,omitempty"`
	PredictType  string `json:"predict_type,omitempty"`
	URL          string `json:"url,omitempty"`
}

// Form contains parameters for upload by multipart/form-data
type Form struct {
	Headers    struct{} `json:"headers"`
	Kind       string   `json:"kind"`   // "drive#form"
	Method     string   `json:"method"` // "POST"
	MultiParts struct {
		OSSAccessKeyID string `json:"OSSAccessKeyId"`
		Signature      string `json:"Signature"`
		Callback       string `json:"callback"`
		Key            string `json:"key"`
		Policy         string `json:"policy"`
		XUserData      string `json:"x:user_data"`
	} `json:"multi_parts"`
	URL string `json:"url"`
}

// Resumable contains parameters for upload by resumable
type Resumable struct {
	Kind     string           `json:"kind,omitempty"`     // "drive#resumable"
	Provider string           `json:"provider,omitempty"` // e.g. "PROVIDER_ALIYUN"
	Params   *ResumableParams `json:"params,omitempty"`
}

// ResumableParams specifies resumable paramegers
type ResumableParams struct {
	AccessKeyID     string `json:"access_key_id,omitempty"`
	AccessKeySecret string `json:"access_key_secret,omitempty"`
	Bucket          string `json:"bucket,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
	Expiration      Time   `json:"expiration,omitempty"`
	Key             string `json:"key,omitempty"`
	SecurityToken   string `json:"security_token,omitempty"`
}

// FileInArchive is a basic element in archive
type FileInArchive struct {
	Index    int    `json:"index,omitempty"`
	Filename string `json:"filename,omitempty"`
	Filesize string `json:"filesize,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Gcid     string `json:"gcid,omitempty"`
	Kind     string `json:"kind,omitempty"`
	IconLink string `json:"icon_link,omitempty"`
	Path     string `json:"path,omitempty"`
}

// ------------------------------------------------------------

// NewFile is a response to RequestNewFile
type NewFile struct {
	File       *File      `json:"file,omitempty"`
	Form       *Form      `json:"form,omitempty"`
	Resumable  *Resumable `json:"resumable,omitempty"`
	Task       *Task      `json:"task,omitempty"`        // null in this case
	UploadType string     `json:"upload_type,omitempty"` // "UPLOAD_TYPE_FORM" or "UPLOAD_TYPE_RESUMABLE"
}

// NewTask is a response to RequestNewTask
type NewTask struct {
	UploadType string `json:"upload_type,omitempty"` // "UPLOAD_TYPE_URL"
	File       *File  `json:"file,omitempty"`        // null in this case
	Task       *Task  `json:"task,omitempty"`
	URL        *URL   `json:"url,omitempty"` // {"kind": "upload#url"}
}

// About informs drive status
type About struct {
	Kind      string   `json:"kind,omitempty"` // "drive#about"
	Quota     *Quota   `json:"quota,omitempty"`
	ExpiresAt string   `json:"expires_at,omitempty"`
	Quotas    struct{} `json:"quotas,omitempty"` // maybe []*Quota?
}

// Quota informs drive quota
type Quota struct {
	Kind           string `json:"kind,omitempty"`                  // "drive#quota"
	Limit          int64  `json:"limit,omitempty,string"`          // limit in bytes
	Usage          int64  `json:"usage,omitempty,string"`          // bytes in use
	UsageInTrash   int64  `json:"usage_in_trash,omitempty,string"` // bytes in trash but this seems not working
	PlayTimesLimit string `json:"play_times_limit,omitempty"`      // maybe in seconds
	PlayTimesUsage string `json:"play_times_usage,omitempty"`      // maybe in seconds
	IsUnlimited    bool   `json:"is_unlimited,omitempty"`
}

// DecompressResult is a response to RequestDecompress
type DecompressResult struct {
	Status       string `json:"status,omitempty"` // "OK"
	StatusText   string `json:"status_text,omitempty"`
	TaskID       string `json:"task_id,omitempty"`   // same as File.Id
	FilesNum     int    `json:"files_num,omitempty"` // number of files in archive
	RedirectLink string `json:"redirect_link,omitempty"`
}

// ------------------------------------------------------------

// RequestBatch is to request for batch actions
type RequestBatch struct {
	IDs []string          `json:"ids,omitempty"`
	To  map[string]string `json:"to,omitempty"`
}

// RequestNewFile is to request for creating a new `drive#folder` or `drive#file`
type RequestNewFile struct {
	// always required
	Kind       string `json:"kind"` // "drive#folder" or "drive#file"
	Name       string `json:"name"`
	ParentID   string `json:"parent_id"`
	FolderType string `json:"folder_type"`
	// only when uploading a new file
	Hash       string            `json:"hash,omitempty"`      // gcid
	Resumable  map[string]string `json:"resumable,omitempty"` // {"provider": "PROVIDER_ALIYUN"}
	Size       int64             `json:"size,omitempty"`
	UploadType string            `json:"upload_type,omitempty"` // "UPLOAD_TYPE_FORM" or "UPLOAD_TYPE_RESUMABLE"
}

// RequestNewTask is to request for creating a new task like offline downloads
//
// Name and ParentID can be left empty.
type RequestNewTask struct {
	Kind       string `json:"kind,omitempty"` // "drive#file"
	Name       string `json:"name,omitempty"`
	ParentID   string `json:"parent_id,omitempty"`
	UploadType string `json:"upload_type,omitempty"` // "UPLOAD_TYPE_URL"
	URL        *URL   `json:"url,omitempty"`         // {"url": downloadUrl}
	FolderType string `json:"folder_type,omitempty"` // "" if parent_id else "DOWNLOAD"
}

// RequestDecompress is to request for decompress of archive files
type RequestDecompress struct {
	Gcid          string           `json:"gcid,omitempty"`     // same as File.Hash
	Password      string           `json:"password,omitempty"` // ""
	FileID        string           `json:"file_id,omitempty"`
	Files         []*FileInArchive `json:"files,omitempty"` // can request selected files to be decompressed
	DefaultParent bool             `json:"default_parent,omitempty"`
}

// ------------------------------------------------------------

// NOT implemented YET

// RequestArchiveFileList is to request for a list of files in archive
//
// POST https://api-drive.mypikpak.com/decompress/v1/list
type RequestArchiveFileList struct {
	Gcid     string `json:"gcid,omitempty"`     // same as api.File.Hash
	Path     string `json:"path,omitempty"`     // "" by default
	Password string `json:"password,omitempty"` // "" by default
	FileID   string `json:"file_id,omitempty"`
}

// ArchiveFileList is a response to RequestArchiveFileList
type ArchiveFileList struct {
	Status      string           `json:"status,omitempty"`       // "OK"
	StatusText  string           `json:"status_text,omitempty"`  // ""
	TaskID      string           `json:"task_id,omitempty"`      // ""
	CurrentPath string           `json:"current_path,omitempty"` // ""
	Title       string           `json:"title,omitempty"`
	FileSize    int64            `json:"file_size,omitempty"`
	Gcid        string           `json:"gcid,omitempty"` // same as File.Hash
	Files       []*FileInArchive `json:"files,omitempty"`
}

// ==============================
var FileNodeType = map[int]string{
	1: "directory",
	2: "file",
	3: "link",
	4: "image",
	5: "pages",
	6: "video",
	7: "audio",
	8: "meeting_minutes",
}

type FileListRequest struct {
	NodeId       string  `json:"node_id"`
	Cursor       *string `json:"cursor,omitempty"`
	Size         *int    `json:"size,omitempty"`
	NeedFullPath bool    `json:"need_full_path"`
}

type FileLinkRequest struct {
	Requests []NodeID `json:"requests"`
}

type NodeID struct {
	NodeID string `json:"node_id"`
}
type NodeName struct {
	NodeName string `json:"node_name"`
}

type ID struct {
	Id string `json:"id"`
}
type BatchNodeList struct {
	NodeList []ID `json:"node_list"`
}

type MoveNodeList struct {
	NodeList        []ID   `json:"node_list"`
	CurrentParentId string `json:"current_parent_id"`
	TargetParnetId  string `json:"target_parent_id"`
}

type RenameNodeRequest struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
}

type CreateDirNode struct {
	LocalID  string `json:"local_id"`
	Name     string `json:"name"`
	ParentID string `json:"parent_id"`
	NodeType int    `json:"node_type"`
}

type CreateDirNodeRequest struct {
	NodeList []CreateDirNode `json:"node_list"`
}

type CreateNodeRequest struct {
	NodeList  []CreateNode `json:"node_list"`
	RequestId string       `json:"request_id"`
}

type CreateNode struct {
	LocalID     string             `json:"local_id"`
	Key         string             `json:"key"`
	Name        string             `json:"name"`
	ParentID    string             `json:"parent_id"`
	NodeType    int                `json:"node_type"`
	NodeContent *map[string]string `json:"node_content"`
	Size        *int64             `json:"size"`
}

type CreateDirNodeRes struct {
	LocalID     string             `json:"local_id"`
	ID          string             `json:"id"`
	Key         string             `json:"key"`
	Name        string             `json:"name"`
	ParentID    string             `json:"parent_id"`
	NodeType    int                `json:"node_type"`
	NodeContent *map[string]string `json:"node_content"`
	Size        *int64             `json:"size"`
}
type CreateDirNodeResponse struct {
	BaseResp
	Data struct {
		NodeList []CreateDirNodeRes `json:"node_list"`
	} `json:"data"`
}

type GetFileUrlRequest struct {
	Uris []string `json:"uris"`
	Type string   `json:"type"`
}

type GetVideoFileUrlRequest struct {
	Key    string `json:"key"`
	NodeID string `json:"node_id"`
}

type BaseResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

type NodeInfoResp struct {
	BaseResp
	Data struct {
		NodeInfo   File   `json:"node_info"`
		Children   []File `json:"children"`
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"data"`
}

type File struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Key                 string `json:"key"`
	NodeType            int    `json:"node_type"` // 0: 文件, 1: 文件夹
	Size                int64  `json:"size"`
	Source              int    `json:"source"`
	NameReviewStatus    int    `json:"name_review_status"`
	ContentReviewStatus int    `json:"content_review_status"`
	RiskReviewStatus    int    `json:"risk_review_status"`
	ConversationID      string `json:"conversation_id"`
	ParentID            string `json:"parent_id"`
	CreateTime          int64  `json:"create_time"`
	UpdateTime          int64  `json:"update_time"`
	Link                *Link  `json:"link,omitempty"`
}

type GetDownloadInfoResp struct {
	BaseResp
	Data struct {
		DownloadInfos []struct {
			NodeID    string `json:"node_id"`
			MainURL   string `json:"main_url"`
			BackupURL string `json:"backup_url"`
		} `json:"download_infos"`
	} `json:"data"`
}

type GetFileUrlResp struct {
	BaseResp
	Data struct {
		FileUrls []struct {
			URI     string `json:"uri"`
			MainURL string `json:"main_url"`
			BackURL string `json:"back_url"`
		} `json:"file_urls"`
	} `json:"data"`
}

type GetVideoFileUrlResp struct {
	BaseResp
	Data struct {
		MediaType string `json:"media_type"`
		MediaInfo []struct {
			Meta struct {
				Height     string  `json:"height"`
				Width      string  `json:"width"`
				Format     string  `json:"format"`
				Duration   float64 `json:"duration"`
				CodecType  string  `json:"codec_type"`
				Definition string  `json:"definition"`
			} `json:"meta"`
			MainURL   string `json:"main_url"`
			BackupURL string `json:"backup_url"`
		} `json:"media_info"`
		OriginalMediaInfo struct {
			Meta struct {
				Height     string  `json:"height"`
				Width      string  `json:"width"`
				Format     string  `json:"format"`
				Duration   float64 `json:"duration"`
				CodecType  string  `json:"codec_type"`
				Definition string  `json:"definition"`
			} `json:"meta"`
			MainURL   string `json:"main_url"`
			BackupURL string `json:"backup_url"`
		} `json:"original_media_info"`
		PosterURL      string `json:"poster_url"`
		PlayableStatus int    `json:"playable_status"`
	} `json:"data"`
}

type UploadNodeResp struct {
	BaseResp
	Data struct {
		NodeList []struct {
			LocalID     string             `json:"local_id"`
			ID          string             `json:"id"`
			ParentID    string             `json:"parent_id"`
			Name        string             `json:"name"`
			Key         string             `json:"key"`
			NodeType    int                `json:"node_type"` // 0: 文件, 1: 文件夹
			NodeContent *map[string]string `json:"node_content"`
		} `json:"node_list"`
	} `json:"data"`
}

type UserInfoResp struct {
	Data    UserInfo `json:"data"`
	Message string   `json:"message"`
}
type AppUserInfo struct {
	BuiAuditInfo string `json:"bui_audit_info"`
}
type AuditInfo struct {
}
type Details struct {
}
type BuiAuditInfo struct {
	AuditInfo      AuditInfo `json:"audit_info"`
	IsAuditing     bool      `json:"is_auditing"`
	AuditStatus    int       `json:"audit_status"`
	LastUpdateTime int64     `json:"last_update_time"`
	UnpassReason   string    `json:"unpass_reason"`
	Details        Details   `json:"details"`
}
type Connects struct {
	Platform           string `json:"platform"`
	ProfileImageURL    string `json:"profile_image_url"`
	ExpiredTime        int    `json:"expired_time"`
	ExpiresIn          int    `json:"expires_in"`
	PlatformScreenName string `json:"platform_screen_name"`
	UserID             int64  `json:"user_id"`
	PlatformUID        string `json:"platform_uid"`
	SecPlatformUID     string `json:"sec_platform_uid"`
	PlatformAppID      int    `json:"platform_app_id"`
	ModifyTime         int    `json:"modify_time"`
	AccessToken        string `json:"access_token"`
	OpenID             string `json:"open_id"`
}
type OperStaffRelationInfo struct {
	HasPassword               int    `json:"has_password"`
	Mobile                    string `json:"mobile"`
	SecOperStaffUserID        string `json:"sec_oper_staff_user_id"`
	RelationMobileCountryCode int    `json:"relation_mobile_country_code"`
}
type UserInfo struct {
	AppID                 int                   `json:"app_id"`
	AppUserInfo           AppUserInfo           `json:"app_user_info"`
	AvatarURL             string                `json:"avatar_url"`
	BgImgURL              string                `json:"bg_img_url"`
	BuiAuditInfo          BuiAuditInfo          `json:"bui_audit_info"`
	CanBeFoundByPhone     int                   `json:"can_be_found_by_phone"`
	Connects              []Connects            `json:"connects"`
	CountryCode           int                   `json:"country_code"`
	Description           string                `json:"description"`
	DeviceID              int                   `json:"device_id"`
	Email                 string                `json:"email"`
	EmailCollected        bool                  `json:"email_collected"`
	Gender                int                   `json:"gender"`
	HasPassword           int                   `json:"has_password"`
	HmRegion              int                   `json:"hm_region"`
	IsBlocked             int                   `json:"is_blocked"`
	IsBlocking            int                   `json:"is_blocking"`
	IsRecommendAllowed    int                   `json:"is_recommend_allowed"`
	IsVisitorAccount      bool                  `json:"is_visitor_account"`
	Mobile                string                `json:"mobile"`
	Name                  string                `json:"name"`
	NeedCheckBindStatus   bool                  `json:"need_check_bind_status"`
	OdinUserType          int                   `json:"odin_user_type"`
	OperStaffRelationInfo OperStaffRelationInfo `json:"oper_staff_relation_info"`
	PhoneCollected        bool                  `json:"phone_collected"`
	RecommendHintMessage  string                `json:"recommend_hint_message"`
	ScreenName            string                `json:"screen_name"`
	SecUserID             string                `json:"sec_user_id"`
	SessionKey            string                `json:"session_key"`
	UseHmRegion           bool                  `json:"use_hm_region"`
	UserCreateTime        int64                 `json:"user_create_time"`
	UserID                int64                 `json:"user_id"`
	UserIDStr             string                `json:"user_id_str"`
	UserVerified          bool                  `json:"user_verified"`
	VerifiedContent       string                `json:"verified_content"`
}

// UploadToken 上传令牌配置
type UploadToken struct {
	Alice    map[string]UploadAuthToken
	Samantha MediaUploadAuthToken
}

// UploadAuthToken 多种类型的上传配置：图片/文件
type UploadAuthToken struct {
	ServiceID        string `json:"service_id"`
	UploadPathPrefix string `json:"upload_path_prefix"`
	Auth             struct {
		AccessKeyID     string    `json:"access_key_id"`
		SecretAccessKey string    `json:"secret_access_key"`
		SessionToken    string    `json:"session_token"`
		ExpiredTime     time.Time `json:"expired_time"`
		CurrentTime     time.Time `json:"current_time"`
	} `json:"auth"`
	UploadHost string `json:"upload_host"`
}

// MediaUploadAuthToken 媒体上传配置
type MediaUploadAuthToken struct {
	StsToken struct {
		AccessKeyID     string    `json:"access_key_id"`
		SecretAccessKey string    `json:"secret_access_key"`
		SessionToken    string    `json:"session_token"`
		ExpiredTime     time.Time `json:"expired_time"`
		CurrentTime     time.Time `json:"current_time"`
	} `json:"sts_token"`
	UploadInfo struct {
		VideoHost string `json:"video_host"`
		SpaceName string `json:"space_name"`
	} `json:"upload_info"`
}

type UploadAuthTokenResp struct {
	BaseResp
	Data UploadAuthToken `json:"data"`
}

type MediaUploadAuthTokenResp struct {
	BaseResp
	Data MediaUploadAuthToken `json:"data"`
}

type ResponseMetadata struct {
	RequestID string `json:"RequestId"`
	Action    string `json:"Action"`
	Version   string `json:"Version"`
	Service   string `json:"Service"`
	Region    string `json:"Region"`
	Error     struct {
		CodeN   int    `json:"CodeN,omitempty"`
		Code    string `json:"Code,omitempty"`
		Message string `json:"Message,omitempty"`
	} `json:"Error,omitempty"`
}

type UploadConfig struct {
	UploadAddress         UploadAddress         `json:"UploadAddress"`
	FallbackUploadAddress FallbackUploadAddress `json:"FallbackUploadAddress"`
	InnerUploadAddress    InnerUploadAddress    `json:"InnerUploadAddress"`
	RequestID             string                `json:"RequestId"`
	SDKParam              interface{}           `json:"SDKParam"`
}

type UploadConfigResp struct {
	ResponseMetadata `json:"ResponseMetadata"`
	Result           UploadConfig `json:"Result"`
}

// StoreInfo 存储信息
type StoreInfo struct {
	StoreURI      string                 `json:"StoreUri"`
	Auth          string                 `json:"Auth"`
	UploadID      string                 `json:"UploadID"`
	UploadHeader  map[string]interface{} `json:"UploadHeader,omitempty"`
	StorageHeader map[string]interface{} `json:"StorageHeader,omitempty"`
}

// UploadAddress 上传地址信息
type UploadAddress struct {
	StoreInfos   []StoreInfo            `json:"StoreInfos"`
	UploadHosts  []string               `json:"UploadHosts"`
	UploadHeader map[string]interface{} `json:"UploadHeader"`
	SessionKey   string                 `json:"SessionKey"`
	Cloud        string                 `json:"Cloud"`
}

// FallbackUploadAddress 备用上传地址
type FallbackUploadAddress struct {
	StoreInfos   []StoreInfo            `json:"StoreInfos"`
	UploadHosts  []string               `json:"UploadHosts"`
	UploadHeader map[string]interface{} `json:"UploadHeader"`
	SessionKey   string                 `json:"SessionKey"`
	Cloud        string                 `json:"Cloud"`
}

// UploadNode 上传节点信息
type UploadNode struct {
	Vid          string                 `json:"Vid"`
	Vids         []string               `json:"Vids"`
	StoreInfos   []StoreInfo            `json:"StoreInfos"`
	UploadHost   string                 `json:"UploadHost"`
	UploadHeader map[string]interface{} `json:"UploadHeader"`
	Type         string                 `json:"Type"`
	Protocol     string                 `json:"Protocol"`
	SessionKey   string                 `json:"SessionKey"`
	NodeConfig   struct {
		UploadMode string `json:"UploadMode"`
	} `json:"NodeConfig"`
	Cluster string `json:"Cluster"`
}

// AdvanceOption 高级选项
type AdvanceOption struct {
	Parallel      int    `json:"Parallel"`
	Stream        int    `json:"Stream"`
	SliceSize     int    `json:"SliceSize"`
	EncryptionKey string `json:"EncryptionKey"`
}

// InnerUploadAddress 内部上传地址
type InnerUploadAddress struct {
	UploadNodes   []UploadNode  `json:"UploadNodes"`
	AdvanceOption AdvanceOption `json:"AdvanceOption"`
}

// UploadPart 上传分片信息
type UploadPart struct {
	UploadId   string `json:"uploadid,omitempty"`
	PartNumber string `json:"part_number,omitempty"`
	Crc32      string `json:"crc32,omitempty"`
	Etag       string `json:"etag,omitempty"`
	Mode       string `json:"mode,omitempty"`
}

// UploadResp 上传响应体
type UploadResp struct {
	Code       int        `json:"code"`
	ApiVersion string     `json:"apiversion"`
	Message    string     `json:"message"`
	Data       UploadPart `json:"data"`
}

type VideoCommitUpload struct {
	Vid       string `json:"Vid"`
	VideoMeta struct {
		URI          string  `json:"Uri"`
		Height       int     `json:"Height"`
		Width        int     `json:"Width"`
		OriginHeight int     `json:"OriginHeight"`
		OriginWidth  int     `json:"OriginWidth"`
		Duration     float64 `json:"Duration"`
		Bitrate      int     `json:"Bitrate"`
		Md5          string  `json:"Md5"`
		Format       string  `json:"Format"`
		Size         int     `json:"Size"`
		FileType     string  `json:"FileType"`
		Codec        string  `json:"Codec"`
	} `json:"VideoMeta"`
	WorkflowInput struct {
		TemplateID string `json:"TemplateId"`
	} `json:"WorkflowInput"`
	GetPosterMode string `json:"GetPosterMode"`
}

type VideoCommitUploadResp struct {
	ResponseMetadata ResponseMetadata `json:"ResponseMetadata"`
	Result           struct {
		RequestID string              `json:"RequestId"`
		Results   []VideoCommitUpload `json:"Results"`
	} `json:"Result"`
}

type CommonResp struct {
	Code    int             `json:"code"`
	Msg     string          `json:"msg,omitempty"`
	Message string          `json:"message,omitempty"` // 错误情况下的消息
	Data    json.RawMessage `json:"data,omitempty"`    // 原始数据,稍后解析
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Locale  string `json:"locale"`
	} `json:"error,omitempty"`
}

// IsSuccess 判断响应是否成功
func (r *CommonResp) IsSuccess() bool {
	return r.Code == 0
}

// GetError 获取错误信息
func (r *CommonResp) GetError() error {
	if r.IsSuccess() {
		return nil
	}
	// 优先使用message字段
	errMsg := r.Message
	if errMsg == "" {
		errMsg = r.Msg
	}
	// 如果error对象存在且有详细消息,则使用error中的信息
	if r.Error != nil && r.Error.Message != "" {
		errMsg = r.Error.Message
	}

	return fmt.Errorf("[doubao] API error (code: %d): %s", r.Code, errMsg)
}

// UnmarshalData 将data字段解析为指定类型
func (r *CommonResp) UnmarshalData(v interface{}) error {
	if !r.IsSuccess() {
		return r.GetError()
	}

	if len(r.Data) == 0 {
		return nil
	}

	return json.Unmarshal(r.Data, v)
}
