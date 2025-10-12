// Package doubao provides an interface to the Doubao
package doubao

// ------------------------------------------------------------
// NOTE
// ------------------------------------------------------------

// md5sum is not always available, sometimes given empty.

// Trashed files are not restored to the original location when using `batchUntrash`

// Can't stream without `--vfs-cache-mode=full`

// ------------------------------------------------------------
// TODO
// ------------------------------------------------------------

// * List() with options starred-only
// * user-configurable list chunk
// * backend command: untrash, iscached
// * api(event,task)

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/fserrors"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/dircache"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/rclone/rclone/lib/pacer"
	"github.com/rclone/rclone/lib/random"
	"github.com/rclone/rclone/lib/rest"
	"github.com/rclone/rclone/plugins/doubao/api"
)

// Constants
const (
	defaultUserAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36"
	downloadAPI         = "get_file_url"
	minSleep            = 100 * time.Millisecond
	maxSleep            = 2 * time.Second
	taskWaitTime        = 500 * time.Millisecond
	decayConstant       = 2 // bigger for slower decay, exponential
	rootURL             = "https://www.doubao.com"
	FileDataType        = "file"
	ImgDataType         = "image"
	VideoDataType       = "video"
	Region              = "cn-north-1"                    // Part number must be an integer between 1 and 10000, inclusive.
	defaultChunkSize    = fs.SizeSuffix(1024 * 1024 * 20) // Part size should be in [100KB, 100M]，默认5m
	minChunkSize        = 100 * fs.Kibi
	maxChunkSize        = 100 * fs.Mebi
	defaultUploadCutoff = fs.SizeSuffix(20 * 1024 * 1024) //超过5m会被分片上传
	maxUploadCutoff     = 100 * fs.Mebi                   // maximum allowed size for singlepart uploads
	durationInMinutes   = 30                              //文件链接有效时长，单位：分钟 可能需要修改
)

// DirectoryType represents a directory node type.
const (
	DirectoryType      = 1
	FileType           = 2
	LinkType           = 3
	ImageType          = 4
	PagesType          = 5
	VideoType          = 6
	AudioType          = 7
	MeetingMinutesType = 8
)

// FileNodeType represents a File node type.
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

// Register with Fs
func init() {
	fs.Register(&fs.RegInfo{
		Name:        "doubao",
		Description: "Doubao",
		NewFs:       NewFs,
		CommandHelp: commandHelp,
		Options: []fs.Option{{
			Name:      "cookie",
			Help:      "Cookie.",
			Required:  true,
			Sensitive: true,
		},
			{
				Name:      "root_folder_id",
				Help:      "根目录ID，通过抓包/samantha/aispace/homepage的响应获取，或文件夹ID",
				Required:  true,
				Sensitive: true,
			},
			{
				Name:      "downloadApi",
				Default:   downloadAPI,
				Help:      "get_file_url,get_download_info",
				Required:  true,
				Sensitive: true,
			},
			{
				Name:      "duration_in_minutes",
				Default:   durationInMinutes,
				Help:      "文件有效期长时整数型默认30分钟",
				Required:  true,
				Sensitive: true,
			}, {
				Name:     "upload_cutoff",
				Help:     `超出这个大小就会进行分片，默认20m和分片大小，可以忽略，豆包单个文件最大100m`,
				Default:  defaultUploadCutoff,
				Advanced: true,
			}, {
				Name:     "chunk_size",
				Help:     `分片大小`,
				Default:  defaultChunkSize,
				Advanced: true,
			}, {
				Name:     config.ConfigEncoding,
				Help:     config.ConfigEncodingHelp,
				Advanced: true,
				Default: (encoder.EncodeCtl |
					encoder.EncodeDot |
					encoder.EncodeBackSlash |
					encoder.EncodeSlash |
					encoder.EncodeDoubleQuote |
					encoder.EncodeAsterisk |
					encoder.EncodeColon |
					encoder.EncodeLtGt |
					encoder.EncodeQuestion |
					encoder.EncodePipe |
					encoder.EncodeLeftSpace |
					encoder.EncodeRightSpace |
					encoder.EncodeRightPeriod |
					encoder.EncodeInvalidUtf8),
			}},
	})
}

// Options defines the configuration for this backend
type Options struct {
	Cookie            string               `config:"cookie"`
	DownloadAPI       string               `config:"downloadApi"`
	UserID            string               `config:"user_id"` // only available during runtime
	DeviceID          string               `config:"device_id"`
	UserAgent         string               `config:"user_agent"`
	RootFolderID      string               `config:"root_folder_id"`
	ChunkSize         fs.SizeSuffix        `config:"chunk_size"`
	UploadCutoff      fs.SizeSuffix        `config:"upload_cutoff"`
	UploadConcurrency int                  `config:"upload_concurrency"`
	Enc               encoder.MultiEncoder `config:"encoding"`
	DurationInMinutes int                  `config:"duration_in_minutes"`
}

// Fs represents a remote pikpak
type Fs struct {
	name           string             // name of this remote
	root           string             // the path we are working on
	opt            Options            // parsed options
	features       *fs.Features       // optional features
	rst            *doubaoClient      // the connection to the server
	dirCache       *dircache.DirCache // Map of directory path to directory id
	pacer          *fs.Pacer          // pacer for API calls
	rootFolderID   string             // the id of the root folder
	client         *http.Client       // authorized client
	m              configmap.Mapper
	tokenMu        *sync.Mutex // when renewing tokens
	uploadToken    *api.UploadToken
	tokenDispenser *pacer.TokenDispenser // control concurrency
	userID         string
}

// Object describes a pikpak object
type Object struct {
	fs          *Fs       // what this object is part of
	remote      string    // The remote path
	hasMetaData bool      // whether info below has been set
	id          string    // ID of the object
	size        int64     // size of the object
	modTime     time.Time // modification time of the object
	mimeType    string    // The object MIME type
	parent      string    // ID of the parent directories
	gcid        string    // custom hash of the object
	md5sum      string    // md5sum of the object
	link        *api.Link // link to download the object
	linkMu      *sync.Mutex
	file        *api.File
}

// ------------------------------------------------------------

// Name of the remote (as passed into NewFs)
func (f *Fs) Name() string {
	return f.name
}

// Root of the remote (as passed into NewFs)
func (f *Fs) Root() string {
	return f.root
}

// String converts this Fs to a string
func (f *Fs) String() string {
	return fmt.Sprintf("Doubao root '%s'", f.root)
}

// Features returns the optional features of this Fs
func (f *Fs) Features() *fs.Features {
	return f.features
}

// Precision return the precision of this Fs
func (f *Fs) Precision() time.Duration {
	return fs.ModTimeNotSupported
	// meaning that the modification times from the backend shouldn't be used for syncing
	// as they can't be set.
}

// DirCacheFlush resets the directory cache - used in testing as an
// optional interface
func (f *Fs) DirCacheFlush() {
	f.dirCache.ResetRoot()
}

// Hashes returns the supported hash sets.
func (f *Fs) Hashes() hash.Set {
	return hash.Set(hash.MD5)
}

// parsePath parses a remote path
func parsePath(path string) (root string) {
	root = strings.Trim(path, "/")
	return
}

// parentIDForRequest returns ParentId for api requests
func parentIDForRequest(dirID string) string {
	if dirID == "root" {
		return ""
	}
	return dirID
}

// retryErrorCodes is a slice of error codes that we will retry
var retryErrorCodes = []int{
	429, // Too Many Requests.
	500, // Internal Server Error
	502, // Bad Gateway
	503, // Service Unavailable
	504, // Gateway Timeout
	509, // Bandwidth Limit Exceeded
}

// shouldRetry returns a boolean as to whether this resp and err
// deserve to be retried.  It returns the err as a convenience
func (f *Fs) shouldRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if fserrors.ContextError(ctx, &err) {
		return false, err
	}
	if err == nil {
		return false, nil
	}
	if fserrors.ShouldRetry(err) {
		return true, err
	}

	return fserrors.ShouldRetryHTTP(resp, retryErrorCodes), err
}

// errorHandler parses a non 2xx error response into an error
// func errorHandler(resp *http.Response) error {
// 	// Decode error response
// 	errResponse := new(api.Error)
// 	err := rest.DecodeJSON(resp, &errResponse)
// 	if err != nil {
// 		fs.Debugf(nil, "Couldn't decode error response: %v", err)
// 	}
// 	if errResponse.Reason == "" {
// 		errResponse.Reason = resp.Status
// 	}
// 	if errResponse.Code == 0 {
// 		errResponse.Code = resp.StatusCode
// 	}
// 	return errResponse
// }

// getClient makes an http client according to the options
func getClient(ctx context.Context, opt *Options) *http.Client {
	// Override few config settings and create a client
	newCtx, ci := fs.AddConfig(ctx)
	ci.UserAgent = opt.UserAgent
	return fshttp.NewClient(newCtx)
}

// newClientWithPacer sets a new http/rest client with a pacer to Fs
func (f *Fs) newClientWithPacer(ctx context.Context) (err error) {
	f.client = getClient(ctx, &f.opt)
	f.rst = newDoubaoClient(f.client, &f.opt)
	f.pacer = fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(minSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant)))
	f.pacer.SetRetries(3)
	return nil
}

func checkUploadChunkSize(cs fs.SizeSuffix) error {
	if cs < minChunkSize {
		return fmt.Errorf("%s is less than %s", cs, minChunkSize)
	}
	if cs > maxChunkSize {
		return fmt.Errorf("%s is greater than %s", cs, maxChunkSize)
	}
	return nil
}

func (f *Fs) setUploadChunkSize(cs fs.SizeSuffix) (old fs.SizeSuffix, err error) {
	err = checkUploadChunkSize(cs)
	if err == nil {
		old, f.opt.ChunkSize = f.opt.ChunkSize, cs
	}
	return
}

func checkUploadCutoff(cs fs.SizeSuffix) error {
	if cs > maxUploadCutoff {
		return fmt.Errorf("%s is greater than %s", cs, maxUploadCutoff)
	}
	return nil
}

func (f *Fs) setUploadCutoff(cs fs.SizeSuffix) (old fs.SizeSuffix, err error) {
	err = checkUploadCutoff(cs)
	if err == nil {
		old, f.opt.UploadCutoff = f.opt.UploadCutoff, cs
	}
	return
}

// newFs partially constructs Fs from the path
//
// It constructs a valid Fs but doesn't attempt to figure out whether
// it is a file or a directory.
func newFs(ctx context.Context, name, path string, m configmap.Mapper) (*Fs, error) {
	// Parse config into Options struct
	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}
	err = checkUploadChunkSize(opt.ChunkSize)
	if err != nil {
		return nil, fmt.Errorf("doubao: chunk size: %w", err)
	}
	err = checkUploadCutoff(opt.UploadCutoff)
	if err != nil {
		return nil, fmt.Errorf("doubao: upload cutoff: %w", err)
	}

	root := parsePath(path)
	ci := fs.GetConfig(ctx)
	f := &Fs{
		name:           name,
		root:           root,
		opt:            *opt,
		m:              m,
		tokenMu:        new(sync.Mutex),
		tokenDispenser: pacer.NewTokenDispenser(ci.Transfers),
	}
	f.features = (&fs.Features{
		WriteMimeType:           false,
		ReadMimeType:            true, // can read the mime type of objects
		CanHaveEmptyDirectories: true, // can have empty directories
		NoMultiThreading:        true, // can't have multiple threads downloading
	}).Fill(ctx, f)

	// new device id if necessary
	if len(f.opt.DeviceID) != 32 {
		f.opt.DeviceID = genDeviceID()
		m.Set("device_id", f.opt.DeviceID)
		fs.Infof(nil, "Using new device id %q", f.opt.DeviceID)
	}

	if err := f.newClientWithPacer(ctx); err != nil {
		return nil, err
	}

	if f.userID == "" {
		userInfo, err := f.getUserInfo(ctx)
		if err != nil {
			return nil, err
		}
		f.userID = strconv.FormatInt(userInfo.UserID, 10)
	}

	if f.uploadToken == nil {
		uploadToken, err := f.initUploadToken(ctx)
		if err != nil {
			return nil, err
		}
		f.uploadToken = uploadToken

	}

	return f, nil
}

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, path string, m configmap.Mapper) (fs.Fs, error) {
	f, err := newFs(ctx, name, path, m)
	if err != nil {
		return nil, err
	}

	// Set the root folder ID
	if f.opt.RootFolderID != "" {
		// use root_folder ID if set
		f.rootFolderID = f.opt.RootFolderID
	} else {
		// pseudo-root
		f.rootFolderID = "root"
	}

	f.dirCache = dircache.New(f.root, f.rootFolderID, f)

	// Find the current root
	err = f.dirCache.FindRoot(ctx, false)
	if err != nil {
		// Assume it is a file
		newRoot, remote := dircache.SplitPath(f.root)
		tempF := *f
		tempF.dirCache = dircache.New(newRoot, f.rootFolderID, &tempF)
		tempF.root = newRoot
		// Make new Fs which is the parent
		err = tempF.dirCache.FindRoot(ctx, false)
		if err != nil {
			// No root so return old f
			return f, nil
		}
		_, err := tempF.NewObject(ctx, remote)
		if err != nil {
			if err == fs.ErrorObjectNotFound {
				// File doesn't exist so return old f
				return f, nil
			}
			return nil, err
		}
		f.features.Fill(ctx, &tempF)
		// XXX: update the old f here instead of returning tempF, since
		// `features` were already filled with functions having *f as a receiver.
		// See https://github.com/rclone/rclone/issues/2182
		f.dirCache = tempF.dirCache
		f.root = tempF.root
		// return an error with an fs which points to the parent
		return f, fs.ErrorIsFile
	}
	return f, nil
}

// readMetaDataForPath reads the metadata from the path
func (f *Fs) readMetaDataForPath(ctx context.Context, path string) (info *api.File, err error) {
	leaf, dirID, err := f.dirCache.FindPath(ctx, path, false)
	if err != nil {
		if err == fs.ErrorDirNotFound {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}

	// checking whether fileObj with name of leaf exists in dirID
	found, err := f.listAll(ctx, dirID, func(item *api.File) bool {
		if item.Name == leaf {
			info = item
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fs.ErrorObjectNotFound
	}
	return info, nil
}

// Return an Object from a path
//
// If it can't be found it returns the error fs.ErrorObjectNotFound.
func (f *Fs) newObjectWithInfo(ctx context.Context, remote string, info *api.File) (fs.Object, error) {
	o := &Object{
		fs:     f,
		remote: remote,
		linkMu: new(sync.Mutex),
		file:   info,
	}
	var err error
	if info != nil {
		err = o.setMetaData(info)
	} else {
		err = o.readMetaData(ctx) // reads info and meta, returning an error
	}
	if err != nil {
		return nil, err
	}
	return o, nil
}

// NewObject finds the Object at remote.  If it can't be found
// it returns the error fs.ErrorObjectNotFound.
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	return f.newObjectWithInfo(ctx, remote, nil)
}

// FindLeaf finds a directory of name leaf in the folder with ID pathID
func (f *Fs) FindLeaf(ctx context.Context, pathID, leaf string) (pathIDOut string, found bool, err error) {
	// Find the leaf in pathID
	found, err = f.listAll(ctx, pathID, func(item *api.File) bool {
		if item.Name == leaf {
			pathIDOut = item.ID
			return true
		}
		return false
	})
	return pathIDOut, found, err
}

// list the objects into the function supplied
//
// If directories is set it only sends directories
// User function to process a File item from listAll
//
// Should return true to finish processing
type listAllFn func(*api.File) bool

// Lists the directory required calling the user function on each item found
//
// If the user fn ever returns true then it early exits with found = true
func (f *Fs) listAll(ctx context.Context, dirID string, fn listAllFn) (found bool, err error) {
	cursor := ""
	pageSize := 30
	opts := rest.Opts{
		Method: "POST",
		Path:   "/samantha/aispace/node_info",
	}
	fileListReq := api.FileListRequest{
		NodeID:       dirID,
		NeedFullPath: true,
	}
OUTER:
	for {
		// 如果有游标，则设置游标和大小
		if cursor != "" {
			fileListReq.Cursor = &cursor
			fileListReq.Size = &pageSize
		} else {
			fileListReq.NeedFullPath = false
		}
		var info api.NodeInfoResp
		var resp *http.Response
		err = f.pacer.Call(func() (bool, error) {
			resp, err = f.rst.CallJSON(ctx, &opts, &fileListReq, &info)
			if err != nil {
				return false, fmt.Errorf("error in CallJSON: %w", err)
			}
			return f.shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return found, fmt.Errorf("couldn't list files: %w", err)
		}
		if len(info.Data.Children) == 0 {
			break
		}
		for _, item := range info.Data.Children {
			item.Name = f.opt.Enc.ToStandardName(item.Name)
			if fn(&item) {
				found = true
				break OUTER
			}
		}
		if info.Data.NextCursor == "" || info.Data.NextCursor == "-1" {
			break
		}
		cursor = info.Data.NextCursor
	}
	return
}

// itemToDirEntry converts a api.File to an fs.DirEntry.
// When the api.File cannot be represented as an fs.DirEntry
// (nil, nil) is returned.
func (f *Fs) itemToDirEntry(ctx context.Context, remote string, item *api.File) (entry fs.DirEntry, err error) {
	switch {
	case item.NodeType == DirectoryType:
		// cache the directory ID for later lookups
		f.dirCache.Put(remote, item.ID)
		d := fs.NewDir(remote, time.Unix(item.UpdateTime, 0)).SetID(item.ID)
		if item.ParentID == "" {
			d.SetParentID("root")
		} else {
			d.SetParentID(item.ParentID)
		}
		return d, nil
	default:
		entry, err = f.newObjectWithInfo(ctx, remote, item)
		if err == fs.ErrorObjectNotFound {
			return nil, nil
		}
		return entry, err
	}
	//return nil, nil
}

// List the objects and directories in dir into entries. The
// entries can be returned in any order but should be for a
// complete directory.
//
// dir should be "" to list the root, and should not have
// trailing slashes.
//
// This should return ErrDirNotFound if the directory isn't
// found.
func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	// fs.Debugf(f, "List(%q)\n", dir)
	dirID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return nil, err
	}
	var iErr error
	_, err = f.listAll(ctx, dirID, func(item *api.File) bool {
		entry, err := f.itemToDirEntry(ctx, path.Join(dir, item.Name), item)
		if err != nil {
			iErr = err
			return true
		}
		if entry != nil {
			entries = append(entries, entry)
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	if iErr != nil {
		return nil, iErr
	}
	return entries, nil
}

// CreateDir makes a directory with pathID as parent and name leaf
func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (newID string, err error) {
	// fs.Debugf(f, "CreateDir(%q, %q)\n", pathID, leaf)
	var createDirNodeList api.CreateDirNodeRequest
	// 将ID数组中的值赋值到 NodeList
	createDirNodeList.NodeList = append(createDirNodeList.NodeList, api.CreateDirNode{
		LocalID:  uuid.New().String(),
		Name:     f.opt.Enc.FromStandardName(leaf),
		ParentID: parentIDForRequest(pathID),
		NodeType: 1,
	})

	info, err := f.requestCreateDirAction(ctx, &createDirNodeList)
	if err != nil {
		return "", err
	}
	return info.Data.NodeList[0].ID, nil
}

// Mkdir creates the container if it doesn't exist
func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	_, err := f.dirCache.FindDir(ctx, dir, true)
	return err
}

// About gets quota information
func (f *Fs) About(ctx context.Context) (usage *fs.Usage, err error) {
	info, err := f.getAbout(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get drive quota: %w", err)
	}
	q := info.Quota
	usage = &fs.Usage{
		Used: fs.NewUsageValue(q.Usage), // bytes in use
		// Trashed: fs.NewUsageValue(q.UsageInTrash), // bytes in trash but this seems not working
	}
	if q.Limit > 0 {
		usage.Total = fs.NewUsageValue(q.Limit)          // quota of bytes that can be used
		usage.Free = fs.NewUsageValue(q.Limit - q.Usage) // bytes which can be uploaded before reaching the quota
	}
	return usage, nil
}

// delete a file or directory by ID w/o using trash
func (f *Fs) deleteObjects(ctx context.Context, IDs []string) (err error) {
	if len(IDs) == 0 {
		return nil
	}
	// 实例化 BatchNodeList
	var batchNodeList api.BatchNodeList
	// 将ID数组中的值赋值到 NodeList
	for _, id := range IDs {
		batchNodeList.NodeList = append(batchNodeList.NodeList, api.ID{ID: id})
	}
	if err := f.requestDeleteAction(ctx, &batchNodeList); err != nil {
		return fmt.Errorf("delete object failed: %w", err)
	}
	return nil
}

// untrash a file or directory by ID
func (f *Fs) untrashObjects(ctx context.Context, IDs []string) (err error) {
	if len(IDs) == 0 {
		return nil
	}
	req := api.RequestBatch{
		IDs: IDs,
	}
	if err := f.requestBatchAction(ctx, "batchUntrash", &req); err != nil {
		return fmt.Errorf("untrash object failed: %w", err)
	}
	return nil
}

// purgeCheck removes the root directory, if check is set then it
// refuses to do so if it has anything in
func (f *Fs) purgeCheck(ctx context.Context, dir string, check bool) error {
	root := path.Join(f.root, dir)
	if root == "" {
		return errors.New("can't purge root directory")
	}
	rootID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}

	if check {
		found, err := f.listAll(ctx, rootID, func(item *api.File) bool {
			fs.Debugf(dir, "Rmdir: contains trashed file: %q", item.Name)
			return false
		})
		if err != nil {
			return err
		}
		if found {
			return fs.ErrorDirectoryNotEmpty
		}
	}
	if root != "" {
		// trash the directory if it had trashed files
		// in or the user wants to trash, otherwise
		// delete it.
		err = f.deleteObjects(ctx, []string{rootID})
		if err != nil {
			return err
		}
	} else if check {
		return errors.New("can't purge root directory")
	}
	f.dirCache.FlushDir(dir)
	if err != nil {
		return err
	}
	return nil
}

// Rmdir deletes the root folder
//
// Returns an error if it isn't empty
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	return f.purgeCheck(ctx, dir, true)
}

// Purge deletes all the files and the container
//
// Optional interface: Only implement this if you have a way of
// deleting all the files quicker than just running Remove() on the
// result of List()
func (f *Fs) Purge(ctx context.Context, dir string) error {
	return f.purgeCheck(ctx, dir, false)
}

// Move the object to a new parent folder
//
// Objects cannot be moved to their current folder.
// "file_move_or_copy_to_cur" (9): Please don't move or copy to current folder or sub folder
//
// If a name collision occurs in the destination folder, PikPak might automatically
// rename the moved item(s) by appending a numbered suffix. For example,
// foo.txt -> foo(1).txt or foo(2).txt if foo(1).txt already exists
func (f *Fs) moveObjects(ctx context.Context, IDs []string, srcID string, dirID string) (err error) {
	if len(IDs) == 0 {
		return nil
	}
	var moveNodeList api.MoveNodeList
	// 将ID数组中的值赋值到 NodeList
	for _, id := range IDs {
		moveNodeList.NodeList = append(moveNodeList.NodeList, api.ID{ID: id})
	}
	moveNodeList.CurrentParentID = srcID
	moveNodeList.TargetParnetID = dirID

	if err := f.requestMoveAction(ctx, "batchMove", &moveNodeList); err != nil {
		return fmt.Errorf("move object failed: %w", err)
	}
	return nil
}

// renames the object
//
// The new name must be different from the current name.
// "file_rename_to_same_name" (3): Name of file or folder is not changed
//
// Within the same folder, object names must be unique.
// "file_duplicated_name" (3): File name cannot be repeated
func (f *Fs) renameObject(ctx context.Context, ID, newName string) (info *api.File, err error) {
	req := api.RenameNodeRequest{
		NodeID:   ID,
		NodeName: f.opt.Enc.FromStandardName(newName),
	}
	if err := f.requestRenameAction(ctx, &req); err != nil {
		return nil, fmt.Errorf("rename object failed: %w", err)
	}
	return
}

// DirMove moves src, srcRemote to this remote at dstRemote
// using server-side move operations.
//
// Will only be called if src.Fs().Name() == f.Name()
//
// If it isn't possible then return fs.ErrorCantDirMove
//
// If destination exists then return fs.ErrorDirExists
func (f *Fs) DirMove(ctx context.Context, src fs.Fs, srcRemote, dstRemote string) error {
	srcFs, ok := src.(*Fs)
	if !ok {
		fs.Debugf(srcFs, "Can't move directory - not same remote type")
		return fs.ErrorCantDirMove
	}

	srcID, srcParentID, srcLeaf, dstParentID, dstLeaf, err := f.dirCache.DirMove(ctx, srcFs.dirCache, srcFs.root, srcRemote, f.root, dstRemote)
	if err != nil {
		return err
	}

	if srcParentID != dstParentID {
		// Do the move
		err = f.moveObjects(ctx, []string{srcID}, srcParentID, dstParentID)
		if err != nil {
			return fmt.Errorf("couldn't dir move: %w", err)
		}
	}

	// Can't copy and change name in one step so we have to check if we have
	// the correct name after copy
	if srcLeaf != dstLeaf {
		_, err = f.renameObject(ctx, srcID, dstLeaf)
		if err != nil {
			return fmt.Errorf("dirmove: couldn't rename moved dir: %w", err)
		}
	}
	srcFs.dirCache.FlushDir(srcRemote)
	return nil
}

// Creates from the parameters passed in a half finished Object which
// must have setMetaData called on it
//
// Returns the object, leaf, dirID and error.
//
// Used to create new objects
func (f *Fs) createObject(ctx context.Context, remote string, modTime time.Time, size int64) (o *Object, leaf string, dirID string, err error) {
	// Create the directory for the object if it doesn't exist
	leaf, dirID, err = f.dirCache.FindPath(ctx, remote, true)
	if err != nil {
		return
	}
	// Temporary Object under construction
	o = &Object{
		fs:      f,
		remote:  remote,
		parent:  dirID,
		size:    size,
		modTime: modTime,
		linkMu:  new(sync.Mutex),
	}
	return o, leaf, dirID, nil
}

// Move src to this remote using server-side move operations.
//
// This is stored with the remote path given.
//
// It returns the destination Object and a possible error.
//
// Will only be called if src.Fs().Name() == f.Name()
//
// If it isn't possible then return fs.ErrorCantMove
func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (dst fs.Object, err error) {
	srcObj, ok := src.(*Object)
	if !ok {
		fs.Debugf(src, "Can't move - not same remote type")
		return nil, fs.ErrorCantMove
	}
	err = srcObj.readMetaData(ctx)
	if err != nil {
		return nil, err
	}

	// Create temporary object - still missing id, mimeType, gcid, md5sum
	dstObj, dstLeaf, dstParentID, err := f.createObject(ctx, remote, srcObj.modTime, srcObj.size)
	if err != nil {
		return nil, err
	}

	if srcObj.parent != dstParentID {
		// Perform the move. A numbered copy might be generated upon name collision.
		if err = f.moveObjects(ctx, []string{srcObj.id}, srcObj.parent, dstParentID); err != nil {
			return nil, fmt.Errorf("move: failed to move object %s to new parent %s: %w", srcObj.id, dstParentID, err)
		}
		defer func() {
			if err != nil {
				// FIXME: Restored file might have a numbered name if a conflict occurs
				if mvErr := f.moveObjects(ctx, []string{srcObj.id}, srcObj.parent, srcObj.parent); mvErr != nil {
					fs.Logf(f, "move: couldn't restore original object %q to %q after move failure: %v", dstObj.id, src.Remote(), mvErr)
				}
			}
		}()
	}

	// Find the moved object and any conflict object with the same name.
	var moved, conflict *api.File
	_, err = f.listAll(ctx, dstParentID, func(item *api.File) bool {
		if item.ID == srcObj.id {
			moved = item
			if item.Name == dstLeaf {
				return true
			}
		} else if item.Name == dstLeaf {
			conflict = item
		}
		// Stop early if both found
		return moved != nil && conflict != nil
	})
	if err != nil {
		return nil, fmt.Errorf("move: couldn't locate moved file %q in destination directory %q: %w", srcObj.id, dstParentID, err)
	}
	if moved == nil {
		return nil, fmt.Errorf("move: moved file %q not found in destination", srcObj.id)
	}

	// If moved object already has the correct name, return
	if moved.Name == dstLeaf {
		return dstObj, dstObj.setMetaData(moved)
	}
	// If name collision, delete conflicting file first
	if conflict != nil {
		if err = f.deleteObjects(ctx, []string{conflict.ID}); err != nil {
			return nil, fmt.Errorf("move: couldn't delete conflicting file: %w", err)
		}
		defer func() {
			if err != nil {
				if restoreErr := f.untrashObjects(ctx, []string{conflict.ID}); restoreErr != nil {
					fs.Logf(f, "move: couldn't restore conflicting file: %v", restoreErr)
				}
			}
		}()
	}
	info, err := f.renameObject(ctx, srcObj.id, dstLeaf)
	if err != nil {
		return nil, fmt.Errorf("move: couldn't rename moved file %q to %q: %w", dstObj.id, dstLeaf, err)
	}
	return dstObj, dstObj.setMetaData(info)
}

// copy objects
//
// Objects cannot be copied to their current folder.
// "file_move_or_copy_to_cur" (9): Please don't move or copy to current folder or sub folder
//
// If a name collision occurs in the destination folder, PikPak might automatically
// rename the copied item(s) by appending a numbered suffix. For example,
// foo.txt -> foo(1).txt or foo(2).txt if foo(1).txt already exists
func (f *Fs) copyObjects(ctx context.Context, IDs []string, dirID string) (err error) {
	if len(IDs) == 0 {
		return nil
	}
	req := api.RequestBatch{
		IDs: IDs,
		To:  map[string]string{"parent_id": parentIDForRequest(dirID)},
	}
	if err := f.requestBatchAction(ctx, "batchCopy", &req); err != nil {
		return fmt.Errorf("copy object failed: %w", err)
	}
	return nil
}

// Copy src to this remote using server side copy operations.
//
// This is stored with the remote path given.
//
// It returns the destination Object and a possible error.
//
// Will only be called if src.Fs().Name() == f.Name()
//
// If it isn't possible then return fs.ErrorCantCopy
func (f *Fs) Copy(ctx context.Context, src fs.Object, remote string) (dst fs.Object, err error) {
	srcObj, ok := src.(*Object)
	if !ok {
		fs.Debugf(src, "Can't copy - not same remote type")
		return nil, fs.ErrorCantCopy
	}
	err = srcObj.readMetaData(ctx)
	if err != nil {
		return nil, err
	}

	// Create temporary object - still missing id, mimeType, gcid, md5sum
	dstObj, dstLeaf, dstParentID, err := f.createObject(ctx, remote, srcObj.modTime, srcObj.size)
	if err != nil {
		return nil, err
	}
	if srcObj.parent == dstParentID {
		// api restriction
		fs.Debugf(src, "Can't copy - same parent")
		return nil, fs.ErrorCantCopy
	}

	// Check for possible conflicts: Pikpak creates numbered copies on name collision.
	var conflict *api.File
	_, srcLeaf := dircache.SplitPath(srcObj.remote)
	if srcLeaf == dstLeaf {
		if conflict, err = f.readMetaDataForPath(ctx, remote); err == nil {
			// delete conflicting file
			if err = f.deleteObjects(ctx, []string{conflict.ID}); err != nil {
				return nil, fmt.Errorf("copy: couldn't delete conflicting file: %w", err)
			}
			defer func() {
				if err != nil {
					if restoreErr := f.untrashObjects(ctx, []string{conflict.ID}); restoreErr != nil {
						fs.Logf(f, "copy: couldn't restore conflicting file: %v", restoreErr)
					}
				}
			}()
		} else if err != fs.ErrorObjectNotFound {
			return nil, err
		}
	} else {
		dstDir, _ := dircache.SplitPath(remote)
		dstObj.remote = path.Join(dstDir, srcLeaf)
		if conflict, err = f.readMetaDataForPath(ctx, dstObj.remote); err == nil {
			tmpName := conflict.Name + "-rclone-copy-" + random.String(8)
			if _, err = f.renameObject(ctx, conflict.ID, tmpName); err != nil {
				return nil, fmt.Errorf("copy: couldn't rename conflicting file: %w", err)
			}
			defer func() {
				if _, renameErr := f.renameObject(ctx, conflict.ID, conflict.Name); renameErr != nil {
					fs.Logf(f, "copy: couldn't rename conflicting file back to original: %v", renameErr)
				}
			}()
		} else if err != fs.ErrorObjectNotFound {
			return nil, err
		}
	}

	// Copy the object
	if err := f.copyObjects(ctx, []string{srcObj.id}, dstParentID); err != nil {
		return nil, fmt.Errorf("couldn't copy file: %w", err)
	}
	err = dstObj.readMetaData(ctx)
	if err != nil {
		return nil, fmt.Errorf("copy: couldn't locate copied file: %w", err)
	}

	if srcLeaf != dstLeaf {
		return f.Move(ctx, dstObj, remote)
	}
	return dstObj, nil
}

func (f *Fs) upload(ctx context.Context, o *Object, in io.Reader, leaf, dirID, gcid string, size int64, options ...fs.OpenOption) (info *api.File, err error) {
	uploadType := api.UploadTypeResumable
	if size >= 0 && size < int64(f.opt.UploadCutoff) {
		uploadType = api.UploadTypeForm
	}

	fileName := f.opt.Enc.FromStandardName(leaf)

	mimeType := "application/octet-stream"
	ext := filepath.Ext(fileName) // 获取文件扩展名
	if ext == "" {
		mimeType = "application/octet-stream" // 如果没有扩展名，返回默认值
	} else {
		// 使用 mime.TypeByExtension 查找 MIME 类型
		fmimeType := mime.TypeByExtension(ext)
		if fmimeType != "" {
			mimeType = fmimeType
		}
	}

	dataType := FileDataType

	switch {
	case strings.HasPrefix(mimeType, "video/"):
		dataType = VideoDataType
	case strings.HasPrefix(mimeType, "audio/"):
		dataType = VideoDataType // 音频与视频使用相同的处理方式
	case strings.HasPrefix(mimeType, "image/"):
		dataType = ImgDataType
	}

	// 获取上传配置
	uploadConfig := api.UploadConfig{}
	if err := f.requestUploadConfig(ctx, &uploadConfig, dataType, fileName, size); err != nil {
		return nil, err
	}

	// 根据uploadType选择上传方式
	if uploadType == api.UploadTypeForm { // 小于1MB，使用普通模式上传
		ups, err := f.requestUpload(ctx, &uploadConfig, in, fileName, dirID, size, dataType, mimeType, options...)
		if err != nil {
			return nil, err
		}
		node := ups.Data.NodeList[0]

		file := api.File{
			ID:       node.ID,
			Name:     node.Name,
			Key:      node.Key,
			NodeType: node.NodeType,
			Size:     size,
			ParentID: node.ParentID,
		}
		return &file, nil
	}

	ups, err := f.requestUploadByMultipart(ctx, &uploadConfig, in, fileName, dirID, size, dataType, mimeType, options...)
	if err != nil {
		return nil, err
	}
	node := ups.Data.NodeList[0]

	file := api.File{
		ID:       node.ID,
		Name:     node.Name,
		Key:      node.Key,
		NodeType: node.NodeType,
		Size:     size,
		ParentID: node.ParentID,
	}
	return &file, nil

}

// Put the object
//
// Copy the reader in to the new object which is returned.
//
// The new object may have been created if an error is returned
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	existingObj, err := f.NewObject(ctx, src.Remote())
	switch err {
	case nil:
		return existingObj, existingObj.Update(ctx, in, src, options...)
	case fs.ErrorObjectNotFound:
		// Not found so create it
		newObj := &Object{
			fs:     f,
			remote: src.Remote(),
			linkMu: new(sync.Mutex),
		}
		return newObj, newObj.upload(ctx, in, src, false, options...)
	default:
		return nil, err
	}
}

// ------------------------------------------------------------

type decompressDirResult struct {
	Decompressed  int
	SourceDeleted int
	Errors        int
}

func (r decompressDirResult) Error() string {
	return fmt.Sprintf("%d error(s) while decompressing - see log", r.Errors)
}

// decompress file/files in a directory of an ID
func (f *Fs) decompressDir(ctx context.Context, filename, id, password string, srcDelete bool) (r decompressDirResult, err error) {
	_, err = f.listAll(ctx, id, func(item *api.File) bool {
		return false
	})
	if err != nil {
		err = fmt.Errorf("couldn't list files to decompress: %w", err)
		r.Errors++
		fs.Errorf(f, "%v", err)
	}
	if r.Errors != 0 {
		return r, r
	}
	return r, nil
}

var commandHelp = []fs.CommandHelp{{
	Name:  "decompress",
	Short: "Request decompress of a file/files in a folder",
	Long: `This command requests decompress of file/files in a folder.

Usage:

    rclone backend decompress doubao:dirpath {filename} -o password=password
    rclone backend decompress doubao:dirpath {filename} -o delete-src-file

An optional argument 'filename' can be specified for a file located in 
'doubao:dirpath'. You may want to pass '-o password=password' for a 
password-protected files. Also, pass '-o delete-src-file' to delete 
source files after decompression finished.

Result:

    {
        "Decompressed": 17,
        "SourceDeleted": 0,
        "Errors": 0
    }
`,
}}

// Command the backend to run a named command
//
// The command run is name
// args may be used to read arguments from
// opts may be used to read optional arguments from
//
// The result should be capable of being JSON encoded
// If it is a string or a []string it will be shown to the user
// otherwise it will be JSON encoded and shown to the user like that
func (f *Fs) Command(ctx context.Context, name string, arg []string, opt map[string]string) (out any, err error) {
	switch name {
	case "decompress":
		filename := ""
		if len(arg) > 0 {
			filename = arg[0]
		}
		id, err := f.dirCache.FindDir(ctx, "", false)
		if err != nil {
			return nil, fmt.Errorf("failed to get an ID of dirpath: %w", err)
		}
		password := ""
		if pass, ok := opt["password"]; ok {
			password = pass
		}
		_, srcDelete := opt["delete-src-file"]
		return f.decompressDir(ctx, filename, id, password, srcDelete)
	default:
		return nil, fs.ErrorCommandNotFound
	}
}

// setMetaData sets the metadata from info
func (o *Object) setMetaData(info *api.File) (err error) {
	if info.NodeType == DirectoryType {
		return fs.ErrorIsDir
	}
	// if info.NodeType == FileType {
	// 	return fmt.Errorf("%q is %q: %w", o.remote, info.NodeType, fs.ErrorNotAFile)
	// }
	o.hasMetaData = true
	o.id = info.ID
	o.size = info.Size
	o.modTime = time.Unix(info.UpdateTime, 0)

	ext := filepath.Ext(info.Name) // 获取文件扩展名
	if ext == "" {
		o.mimeType = "application/octet-stream" // 如果没有扩展名，返回默认值
	} else {
		// 使用 mime.TypeByExtension 查找 MIME 类型
		mimeType := mime.TypeByExtension(ext)
		if mimeType == "" {
			o.mimeType = "application/octet-stream" // 如果无法找到对应 MIME 类型，返回默认值
		} else {
			o.mimeType = mimeType
		}
	}

	if info.ParentID == "" {
		o.parent = "root"
	} else {
		o.parent = info.ParentID
	}
	o.gcid = info.ID

	if info.Link != nil {
		o.link = info.Link
	}
	return nil
}

// setMetaDataWithLink ensures a link for opening an object
func (o *Object) setMetaDataWithLink(ctx context.Context) error {
	o.linkMu.Lock()
	defer o.linkMu.Unlock()
	if o.link != nil && o.link.Valid() {
		return nil
	}

	info, err := o.fs.getFile(ctx, o)
	if err != nil {
		return err
	}
	return o.setMetaData(info)
}

// readMetaData gets the metadata if it hasn't already been fetched
//
// it also sets the info
func (o *Object) readMetaData(ctx context.Context) (err error) {
	if o.hasMetaData {
		return nil
	}
	info, err := o.fs.readMetaDataForPath(ctx, o.remote)
	if err != nil {
		return err
	}
	return o.setMetaData(info)
}

// Fs returns the parent Fs
func (o *Object) Fs() fs.Info {
	return o.fs
}

// Return a string version
func (o *Object) String() string {
	if o == nil {
		return "<nil>"
	}
	return o.remote
}

// Remote returns the remote path
func (o *Object) Remote() string {
	return o.remote
}

// Hash returns the Md5sum of an object returning a lowercase hex string
func (o *Object) Hash(ctx context.Context, t hash.Type) (string, error) {
	if t != hash.MD5 {
		return "", hash.ErrUnsupported
	}
	return strings.ToLower(o.md5sum), nil
}

// Size returns the size of an object in bytes
func (o *Object) Size() int64 {
	err := o.readMetaData(context.TODO())
	if err != nil {
		fs.Logf(o, "Failed to read metadata: %v", err)
		return 0
	}
	return o.size
}

// MimeType of an Object if known, "" otherwise
func (o *Object) MimeType(ctx context.Context) string {
	return o.mimeType
}

// ID returns the ID of the Object if known, or "" if not
func (o *Object) ID() string {
	return o.id
}

// ParentID returns the ID of the Object parent if known, or "" if not
func (o *Object) ParentID() string {
	return o.parent
}

// ModTime returns the modification time of the object
func (o *Object) ModTime(ctx context.Context) time.Time {
	err := o.readMetaData(ctx)
	if err != nil {
		fs.Logf(o, "Failed to read metadata: %v", err)
		return time.Now()
	}
	return o.modTime
}

// SetModTime sets the modification time of the local fs object
func (o *Object) SetModTime(ctx context.Context, modTime time.Time) error {
	return fs.ErrorCantSetModTime
}

// Storable returns a boolean showing whether this object storable
func (o *Object) Storable() bool {
	return true
}

// Remove an object
func (o *Object) Remove(ctx context.Context) error {
	return o.fs.deleteObjects(ctx, []string{o.id})
}

// httpResponse gets an http.Response object for the object
// using the url and method passed in
func (o *Object) httpResponse(ctx context.Context, url, method string, options []fs.OpenOption) (res *http.Response, err error) {
	if url == "" {
		return nil, errors.New("forbidden to download - check sharing permission")
	}
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	fs.FixRangeOption(options, o.size)
	fs.OpenOptionAddHTTPHeaders(req.Header, options)
	if o.size == 0 {
		// Don't supply range requests for 0 length objects as they always fail
		delete(req.Header, "Range")
	}
	err = o.fs.pacer.Call(func() (bool, error) {
		res, err = o.fs.client.Do(req)
		return o.fs.shouldRetry(ctx, res, err)
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// open a url for reading
func (o *Object) open(ctx context.Context, url string, options ...fs.OpenOption) (in io.ReadCloser, err error) {
	res, err := o.httpResponse(ctx, url, "GET", options)
	if err != nil {
		return nil, fmt.Errorf("open file failed: %w", err)
	}
	return res.Body, nil
}

// Open an object for read
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (in io.ReadCloser, err error) {
	if o.id == "" {
		return nil, errors.New("can't download: no id")
	}
	if o.size == 0 {
		// zero-byte objects may have no download link
		return io.NopCloser(bytes.NewBuffer([]byte(nil))), nil
	}

	if o.link != nil && o.link.Valid() {
		return o.open(ctx, o.link.URL, options...)
	}

	if err = o.setMetaDataWithLink(ctx); err != nil {
		return nil, fmt.Errorf("can't download: %w", err)
	}
	return o.open(ctx, o.link.URL, options...)

}

// Update the object with the contents of the io.Reader, modTime and size
//
// If existing is set then it updates the object rather than creating a new one.
//
// The new object may have been created if an error is returned
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (err error) {
	return o.upload(ctx, in, src, true, options...)
}

// upload uploads the object with or without using a temporary file name
func (o *Object) upload(ctx context.Context, in io.Reader, src fs.ObjectInfo, withTemp bool, options ...fs.OpenOption) (err error) {
	size := src.Size()
	remote := o.Remote()

	// Create the directory for the object if it doesn't exist
	leaf, dirID, err := o.fs.dirCache.FindPath(ctx, remote, true)
	if err != nil {
		return err
	}

	gcid, err := o.fs.getGcid(ctx, src)
	if err != nil || gcid == "" {
		fs.Debugf(o, "calculating gcid: %v", err)
		if srcObj := unWrapObjectInfo(src); srcObj != nil && srcObj.Fs().Features().IsLocal {
			// No buffering; directly calculate gcid from source
			rc, err := srcObj.Open(ctx)
			if err != nil {
				return fmt.Errorf("failed to open src: %w", err)
			}
			defer fs.CheckClose(rc, &err)

			if gcid, err = calcGcid(rc, srcObj.Size()); err != nil {
				return fmt.Errorf("failed to calculate gcid: %w", err)
			}
		} else {
			var cleanup func()
			gcid, in, cleanup, err = readGcid(in, size, int64(4))
			defer cleanup()
			if err != nil {
				return fmt.Errorf("failed to calculate gcid: %w", err)
			}
		}
	}
	fs.Debugf(o, "gcid = %s", gcid)

	if !withTemp {
		info, err := o.fs.upload(ctx, o, in, leaf, dirID, gcid, size, options...)
		if err != nil {
			return err
		}
		return o.setMetaData(info)
	}

	// We have to fall back to upload + rename
	tempName := "rcloneTemp" + random.String(8)
	info, err := o.fs.upload(ctx, o, in, tempName, dirID, gcid, size, options...)
	if err != nil {
		return err
	}

	// upload was successful, need to delete old object before rename
	if err = o.Remove(ctx); err != nil {
		return fmt.Errorf("failed to remove old object: %w", err)
	}

	// rename also updates metadata
	if info, err = o.fs.renameObject(ctx, info.ID, leaf); err != nil {
		return fmt.Errorf("failed to rename temp object: %w", err)
	}
	return o.setMetaData(info)
}

// Check the interfaces are satisfied
var (
	// _ fs.ListRer         = (*Fs)(nil)
	// _ fs.ChangeNotifier  = (*Fs)(nil)
	// _ fs.PutStreamer     = (*Fs)(nil)
	_ fs.Fs              = (*Fs)(nil)
	_ fs.Purger          = (*Fs)(nil)
	_ fs.Copier          = (*Fs)(nil)
	_ fs.Mover           = (*Fs)(nil)
	_ fs.DirMover        = (*Fs)(nil)
	_ fs.Commander       = (*Fs)(nil)
	_ fs.DirCacheFlusher = (*Fs)(nil)
	_ fs.Abouter         = (*Fs)(nil)
	_ fs.Object          = (*Object)(nil)
	_ fs.MimeTyper       = (*Object)(nil)
	_ fs.IDer            = (*Object)(nil)
	_ fs.ParentIDer      = (*Object)(nil)
)
