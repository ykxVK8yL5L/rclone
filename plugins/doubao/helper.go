package doubao

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	stdpath "path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/lib/rest"
	"github.com/rclone/rclone/plugins/doubao/api"
)

// Globals
const (
	cachePrefix = "rclone-doubao-gcid-"
)

// requestBatchAction requests batch actions to API
//
// action can be one of batch{Copy,Delete,Trash,Untrash}
func (f *Fs) requestBatchAction(ctx context.Context, action string, req *api.RequestBatch) (err error) {
	opts := rest.Opts{
		Method: "POST",
		Path:   "/samantha/aispace/move_node" + action,
	}
	info := struct {
		TaskID string `json:"task_id"`
	}{}
	var resp *http.Response
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.rst.CallJSON(ctx, &opts, &req, &info)
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return fmt.Errorf("batch action %q failed: %w", action, err)
	}
	return f.waitTask(ctx, info.TaskID)
}

func (f *Fs) requestDeleteAction(ctx context.Context, req *api.BatchNodeList) (err error) {
	opts := rest.Opts{
		Method: "POST",
		Path:   "/samantha/aispace/delete_node",
	}

	var info api.BaseResp

	var resp *http.Response
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.rst.CallJSON(ctx, &opts, &req, &info)
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return fmt.Errorf("delete action failed: %w", err)
	}
	return nil
}

func (f *Fs) requestMoveAction(ctx context.Context, action string, req *api.MoveNodeList) (err error) {
	opts := rest.Opts{
		Method: "POST",
		Path:   "/samantha/aispace/move_node",
	}

	var info api.BaseResp

	var resp *http.Response
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.rst.CallJSON(ctx, &opts, &req, &info)
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return fmt.Errorf("batch action %q failed: %w", action, err)
	}
	return nil
}

func (f *Fs) requestRenameAction(ctx context.Context, req *api.RenameNodeRequest) (err error) {
	opts := rest.Opts{
		Method: "POST",
		Path:   "/samantha/aispace/rename_node",
	}

	var info api.BaseResp

	var resp *http.Response
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.rst.CallJSON(ctx, &opts, &req, &info)
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return fmt.Errorf("batch action failed: %w", err)
	}
	return nil
}

func (f *Fs) requestCreateDirAction(ctx context.Context, req *api.CreateDirNodeRequest) (createRes *api.CreateDirNodeResponse, err error) {
	opts := rest.Opts{
		Method: "POST",
		Path:   "/samantha/aispace/upload_node",
	}
	var info *api.CreateDirNodeResponse
	var resp *http.Response
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.rst.CallJSON(ctx, &opts, &req, &info)
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	return info, nil
}

func (f *Fs) requestUpload(ctx context.Context, config *api.UploadConfig, in io.Reader, fileName string, dirID string, size int64, dataType string, mimeType string, options ...fs.OpenOption) (noderes *api.UploadNodeResp, rerr error) {
	var err error
	// 计算CRC32
	// 创建 CRC32 哈希实例
	data, err := io.ReadAll(in)
	if err != nil {
		fs.Fatal("Error reading input:", err.Error())
	}
	crc32Hash := crc32.NewIEEE()
	crc32Hash.Write(data)
	crc32Value := hex.EncodeToString(crc32Hash.Sum(nil))

	// 构建请求路径
	uploadNode := config.InnerUploadAddress.UploadNodes[0]
	storeInfo := uploadNode.StoreInfos[0]
	//uploadUrl := fmt.Sprintf("https://%s/upload/v1/%s", uploadNode.UploadHost, storeInfo.StoreURI)
	baseURL := fmt.Sprintf("https://%s", uploadNode.UploadHost)
	pathURL := fmt.Sprintf("/upload/v1/%s", storeInfo.StoreURI)

	extraHeaders := map[string]string{
		"Referer":             rootURL + "/",
		"Origin":              rootURL,
		"User-Agent":          defaultUserAgent,
		"X-Storage-U":         f.userID,
		"Authorization":       storeInfo.Auth,
		"Content-Type":        "application/octet-stream",
		"Content-Crc32":       crc32Value,
		"Content-Length":      fmt.Sprintf("%d", size),
		"Content-Disposition": fmt.Sprintf("attachment; filename=%s", url.QueryEscape(storeInfo.StoreURI)),
	}

	if err != nil {
		return
	}

	newReader := bytes.NewReader(data)
	opts := rest.Opts{
		RootURL:      baseURL,
		Path:         pathURL,
		Method:       "POST",
		ExtraHeaders: extraHeaders,
		Body:         newReader,
	}

	var resp *http.Response
	var info api.UploadResp
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.rst.Call(ctx, &opts)
		if err != nil {
			time.Sleep(5 * time.Second)
			return true, errors.New("上传失败")
		}
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	bytes, _ := io.ReadAll(resp.Body)
	err = json.Unmarshal(bytes, &info)
	if err != nil {
		fs.Fatalf("格式化响应到实例错误: %v", err.Error())
		return nil, err
	}

	if info.Code != 2000 {
		return nil, fmt.Errorf("upload part failed: %s", info.Message)
	} else if info.Data.Crc32 != crc32Value {
		return nil, fmt.Errorf("upload part failed: crc32 mismatch, expected %s, got %s", crc32Value, info.Data.Crc32)
	}

	uploadNodeResp, err := f.uploadNode(ctx, config, dirID, fileName, size, dataType, mimeType)
	if err != nil {
		return nil, err
	}

	return uploadNodeResp, nil
}

func (f *Fs) requestUploadByMultipart(ctx context.Context, config *api.UploadConfig, in io.Reader, fileName string, dirID string, size int64, dataType string, mimeType string, options ...fs.OpenOption) (noderes *api.UploadNodeResp, rerr error) {
	// 构建请求路径
	uploadNode := config.InnerUploadAddress.UploadNodes[0]
	storeInfo := uploadNode.StoreInfos[0]
	uploadURL := fmt.Sprintf("https://%s/upload/v1/%s", uploadNode.UploadHost, storeInfo.StoreURI)
	// 初始化分片上传
	uploadID, err := f.initMultipartUpload(ctx, config, uploadURL, storeInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize multipart upload: %w", err)
	}

	// 准备分片参数
	chunkSize := int64(f.opt.ChunkSize)
	if config.InnerUploadAddress.AdvanceOption.SliceSize > 0 {
		chunkSize = int64(config.InnerUploadAddress.AdvanceOption.SliceSize)
	}

	totalParts := (size + chunkSize - 1) / chunkSize
	// 创建分片信息组
	parts := make([]api.UploadPart, totalParts)

	in, _ = accounting.UnWrap(in)

	// Upload the chunks
	remaining := size
	position := int64(0)
	crc32Hash := crc32.NewIEEE()
	errs := make(chan error, 1)
	var wg sync.WaitGroup
outer:
	for part := range totalParts {
		// Check any errors
		select {
		case err = <-errs:
			break outer
		default:
		}

		partNumber := part + 1 // 分片编号从1开始
		reqSize := min(remaining, chunkSize)
		// Make a block of memory
		buf := make([]byte, reqSize)

		// Read the chunk
		_, err = io.ReadFull(in, buf)
		if err != nil {
			err = fmt.Errorf("multipart upload failed to read source: %w", err)
			break outer
		}

		crc32Value := ""
		crc32Hash.Reset()
		// Make the global hash (must be done sequentially)
		_, _ = crc32Hash.Write(buf)
		crc32Value = hex.EncodeToString(crc32Hash.Sum(nil))

		// Transfer the chunk
		wg.Add(1)
		f.tokenDispenser.Get()
		go func(part int64, position int64) {
			defer wg.Done()
			defer f.tokenDispenser.Put()

			// 构建请求路径
			//uploadUrl := fmt.Sprintf("https://%s/upload/v1/%s", uploadNode.UploadHost, storeInfo.StoreURI)
			baseURL := fmt.Sprintf("https://%s", uploadNode.UploadHost)
			pathURL := fmt.Sprintf("/upload/v1/%s", storeInfo.StoreURI)

			params := url.Values{}
			params.Set("uploadid", uploadID)
			params.Set("part_number", strconv.FormatInt(partNumber, 10))
			params.Set("phase", "transfer")

			extraHeaders := map[string]string{
				"Referer":             rootURL + "/",
				"Origin":              rootURL,
				"User-Agent":          defaultUserAgent,
				"X-Storage-U":         f.userID,
				"Authorization":       storeInfo.Auth,
				"Content-Type":        "application/octet-stream",
				"Content-Crc32":       crc32Value,
				"Content-Length":      fmt.Sprintf("%d", reqSize),
				"Content-Disposition": fmt.Sprintf("attachment; filename=%s", url.QueryEscape(storeInfo.StoreURI)),
			}

			newReader := bytes.NewReader(buf)
			opts := rest.Opts{
				Method:        "POST",
				RootURL:       baseURL,
				Path:          pathURL,
				ExtraHeaders:  extraHeaders,
				Body:          newReader,
				Parameters:    params,
				ContentLength: &reqSize,
			}

			var resp *http.Response
			var info api.UploadResp
			err = f.pacer.Call(func() (bool, error) {
				resp, err = f.rst.Call(ctx, &opts)
				if err != nil {
					time.Sleep(5 * time.Second)
					return true, errors.New("上传失败")
				}
				return f.shouldRetry(ctx, resp, err)
			})
			if err != nil {
				err = fmt.Errorf("multipart upload failed to upload %d part: %w", partNumber, err)
				select {
				case errs <- err:
				default:
				}
				return
			}
			bytes, _ := io.ReadAll(resp.Body)
			err = json.Unmarshal(bytes, &info)

			if err != nil {
				err = fmt.Errorf("格式化响应到实例错误: %w", err)
				select {
				case errs <- err:
				default:
				}
				return
			}

			if info.Code != 2000 {
				err = fmt.Errorf("upload part %d failed: %s", partNumber, info.Message)
				select {
				case errs <- err:
				default:
				}
				return
			} else if info.Data.Crc32 != crc32Value {
				err = fmt.Errorf("upload part failed: crc32 mismatch, expected %s, got %s", crc32Value, info.Data.Crc32)
				select {
				case errs <- err:
				default:
				}
				return
			}

			parts[part] = api.UploadPart{
				PartNumber: strconv.FormatInt(partNumber, 10),
				Etag:       info.Data.Etag,
				Crc32:      crc32Value,
			}
		}(part, position)
		// ready for next block
		remaining -= chunkSize
		position += chunkSize
	}
	wg.Wait()
	if err == nil {
		select {
		case err = <-errs:
		default:
		}
	}
	if err != nil {
		return nil, err
	}

	// 完成上传-分片合并
	if err = f.completeMultipartUpload(ctx, config, uploadURL, uploadID, parts); err != nil {
		return nil, fmt.Errorf("failed to complete multipart upload: %w", err)
	}
	// 提交上传

	if err = f.commitMultipartUpload(ctx, config); err != nil {
		return nil, fmt.Errorf("failed to commit upload: %w", err)
	}

	uploadNodeResp, err := f.uploadNode(ctx, config, dirID, fileName, size, dataType, mimeType)
	if err != nil {
		return nil, err
	}

	return uploadNodeResp, nil

}

func (f *Fs) completeMultipartUpload(ctx context.Context, config *api.UploadConfig, uploadURL, uploadID string, parts []api.UploadPart) error {
	var err error
	storeInfo := config.InnerUploadAddress.UploadNodes[0].StoreInfos[0]
	body := _convertUploadParts(parts)

	// 构建请求路径
	uploadNode := config.InnerUploadAddress.UploadNodes[0]
	//uploadUrl := fmt.Sprintf("https://%s/upload/v1/%s", uploadNode.UploadHost, storeInfo.StoreURI)
	baseURL := fmt.Sprintf("https://%s", uploadNode.UploadHost)
	pathURL := fmt.Sprintf("/upload/v1/%s", storeInfo.StoreURI)

	extraHeaders := map[string]string{
		"Host":          strings.Split(uploadURL, "/")[2],
		"Referer":       baseURL + "/",
		"Origin":        baseURL,
		"User-Agent":    f.opt.UserAgent,
		"X-Storage-U":   f.userID,
		"Authorization": storeInfo.Auth,
		"Content-Type":  "text/plain;charset=UTF-8",
	}

	params := url.Values{}
	params.Set("uploadid", uploadID)
	params.Set("uploadmode", "part")
	params.Set("phase", "finish")

	newReader := strings.NewReader(body)
	opts := rest.Opts{
		RootURL:      baseURL,
		Path:         pathURL,
		Method:       "POST",
		ExtraHeaders: extraHeaders,
		Body:         newReader,
		Parameters:   params,
	}

	var resp *http.Response
	var info api.UploadResp
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.rst.Call(ctx, &opts)
		if err != nil {
			time.Sleep(5 * time.Second)
			return true, errors.New("completeMultipartUpload失败")
		}
		return f.shouldRetry(ctx, resp, err)
	})

	if err != nil {
		return err
	}

	bytes, _ := io.ReadAll(resp.Body)
	err = json.Unmarshal(bytes, &info)

	if err != nil {
		return fmt.Errorf("failed to complete multipart upload: %w", err)
	}

	return nil
}

func (f *Fs) commitMultipartUpload(ctx context.Context, uploadConfig *api.UploadConfig) error {
	var err error
	uploadURL := f.uploadToken.Samantha.UploadInfo.VideoHost

	params := map[string]string{
		"Action":    "CommitUploadInner",
		"Version":   "2020-11-19",
		"SpaceName": f.uploadToken.Samantha.UploadInfo.SpaceName,
	}
	tokenType := VideoDataType
	videoCommitUploadResp := api.VideoCommitUploadResp{}
	paramsValues := url.Values{}
	for key, value := range params {
		paramsValues.Add(key, value)
	}
	req := map[string]any{
		"SessionKey": uploadConfig.InnerUploadAddress.UploadNodes[0].SessionKey,
		"Functions":  []map[string]string{},
	}

	extraHeaders := map[string]string{
		"Content-Type": "application/json",
	}

	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	// 使用 bytes.Buffer 创建一个 io.Reader
	newReader := bytes.NewReader(data)

	opts := rest.Opts{
		Method:       "POST",
		RootURL:      uploadURL,
		Parameters:   paramsValues,
		ExtraHeaders: extraHeaders,
		Body:         newReader,
	}

	_, err = f.requestAPI(ctx, opts, tokenType, &videoCommitUploadResp, nil)

	if err != nil {
		return err
	}
	return nil
}

// _convertUploadParts 将分片信息转换为字符串
func _convertUploadParts(parts []api.UploadPart) string {
	if len(parts) == 0 {
		return ""
	}

	var result strings.Builder

	for i, part := range parts {
		if i > 0 {
			result.WriteString(",")
		}
		result.WriteString(fmt.Sprintf("%s:%s", part.PartNumber, part.Crc32))
	}

	return result.String()
}

func (f *Fs) initMultipartUpload(ctx context.Context, config *api.UploadConfig, uploadURL string, storeInfo api.StoreInfo) (uploadID string, err error) {
	// 构建请求路径
	uploadNode := config.InnerUploadAddress.UploadNodes[0]
	baseURL := fmt.Sprintf("https://%s", uploadNode.UploadHost)
	pathURL := fmt.Sprintf("/upload/v1/%s", storeInfo.StoreURI)
	extraHeaders := map[string]string{
		"Host":          strings.Split(uploadURL, "/")[2],
		"Referer":       baseURL + "/",
		"Origin":        baseURL,
		"User-Agent":    f.opt.UserAgent,
		"X-Storage-U":   f.userID,
		"Authorization": storeInfo.Auth,
		"Content-Type":  "text/plain;charset=UTF-8",
	}

	params := url.Values{}
	params.Set("uploadmode", "part")
	params.Set("phase", "init")

	opts := rest.Opts{
		RootURL:      baseURL,
		Method:       "POST",
		Path:         pathURL,
		ExtraHeaders: extraHeaders,
		Parameters:   params,
	}

	var resp *http.Response
	var info api.UploadResp
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.rst.Call(ctx, &opts)
		if err != nil {
			time.Sleep(5 * time.Second)
			return true, errors.New("初始化上传信息失败")
		}
		return f.shouldRetry(ctx, resp, err)
	})

	bytes, _ := io.ReadAll(resp.Body)
	err = json.Unmarshal(bytes, &info)

	if err != nil {
		return uploadID, err
	}

	if info.Code != 2000 {
		return uploadID, fmt.Errorf("init upload failed: %s", info.Message)
	}

	return info.Data.UploadID, nil
}

// uploadNode 上传 文件信息
func (f *Fs) uploadNode(ctx context.Context, uploadConfig *api.UploadConfig, dirID string, fileName string, fileSize int64, dataType string, mimeType string) (*api.UploadNodeResp, error) {
	var err error
	var key string
	var nodeType int

	switch dataType {
	case VideoDataType:
		key = uploadConfig.InnerUploadAddress.UploadNodes[0].Vid
		if strings.HasPrefix(mimeType, "audio/") {
			nodeType = AudioType // 音频类型
		} else {
			nodeType = VideoType // 视频类型
		}
	case ImgDataType:
		key = uploadConfig.InnerUploadAddress.UploadNodes[0].StoreInfos[0].StoreURI
		nodeType = ImageType // 图片类型
	default: // FileDataType
		key = uploadConfig.InnerUploadAddress.UploadNodes[0].StoreInfos[0].StoreURI
		nodeType = FileType // 文件类型
	}

	var resp *http.Response
	var info api.UploadNodeResp
	opts := rest.Opts{
		Method: "POST",
		Path:   "/samantha/aispace/upload_node",
	}

	requuid := uuid.New().String()
	var createDirNodeList api.CreateNodeRequest
	// 将ID数组中的值赋值到 NodeList
	createDirNodeList.NodeList = append(createDirNodeList.NodeList, api.CreateNode{
		LocalID:     requuid,
		Name:        fileName,
		ParentID:    dirID,
		NodeType:    nodeType,
		Key:         key,
		NodeContent: &map[string]string{},
		Size:        &fileSize,
	})
	createDirNodeList.RequestID = requuid

	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.rst.CallJSON(ctx, &opts, &createDirNodeList, &info)
		if err != nil {
			time.Sleep(5 * time.Second)
			return true, errors.New("创建节点请求失败")
		}
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}

	return &info, err
}

func (f *Fs) getUserInfo(ctx context.Context) (ut *api.UserInfo, err error) {
	opts := rest.Opts{
		Method: "GET",
		Path:   "/passport/account/info/v2/",
	}
	var resp *http.Response
	var info api.UserInfoResp
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.rst.CallJSON(ctx, &opts, nil, &info)
		if err != nil {
			time.Sleep(5 * time.Second)
			return true, errors.New("get_user_info请求失败")
		}
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}

	return &info.Data, err

}

func (f *Fs) initUploadToken(ctx context.Context) (ut *api.UploadToken, err error) {

	uploadToken := &api.UploadToken{
		Alice:    make(map[string]api.UploadAuthToken),
		Samantha: api.MediaUploadAuthToken{},
	}

	fileAuthToken, err := f.getUploadAuthToken(ctx, FileDataType)
	if err != nil {
		return nil, err
	}

	imgAuthToken, err := f.getUploadAuthToken(ctx, ImgDataType)
	if err != nil {
		return nil, err
	}

	mediaAuthToken, err := f.getSamantaUploadAuthToken(ctx)
	if err != nil {
		return nil, err
	}

	uploadToken.Alice[FileDataType] = *fileAuthToken
	uploadToken.Alice[ImgDataType] = *imgAuthToken
	uploadToken.Samantha = *mediaAuthToken

	return uploadToken, nil
}

func (f *Fs) getUploadAuthToken(ctx context.Context, dataType string) (ut *api.UploadAuthToken, err error) {
	opts := rest.Opts{
		Method: "POST",
		Path:   "/alice/upload/auth_token",
	}
	req := map[string]string{
		"scene":     "bot_chat",
		"data_type": dataType,
	}

	var resp *http.Response
	var info api.UploadAuthTokenResp
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.rst.CallJSON(ctx, &opts, &req, &info)
		if err != nil {
			time.Sleep(5 * time.Second)
			return true, errors.New("get_download_info没有找到文件下载链接")
		}
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}

	return &info.Data, err

}

func (f *Fs) getSamantaUploadAuthToken(ctx context.Context) (mt *api.MediaUploadAuthToken, err error) {
	opts := rest.Opts{
		Method: "POST",
		Path:   "/samantha/media/get_upload_token",
	}
	req := map[string]string{}

	var resp *http.Response
	var info api.MediaUploadAuthTokenResp
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.rst.CallJSON(ctx, &opts, &req, &info)
		if err != nil {
			time.Sleep(5 * time.Second)
			return true, errors.New("get_download_info没有找到文件下载链接")
		}
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}

	return &info.Data, err
}

// requestNewFile requests a new api.NewFile and returns api.File
func (f *Fs) requestUploadConfig(ctx context.Context, config *api.UploadConfig, dataType string, fileName string, fileSize int64) (err error) {
	tokenType := dataType
	// 配置参数函数
	configureParams := func() (string, map[string]string) {
		var uploadURL string
		var params map[string]string
		// 根据数据类型设置不同的上传参数
		switch dataType {
		case VideoDataType:
			// 音频/视频类型 - 使用uploadToken.Samantha的配置
			uploadURL = f.uploadToken.Samantha.UploadInfo.VideoHost
			params = map[string]string{
				"Action":       "ApplyUploadInner",
				"Version":      "2020-11-19",
				"SpaceName":    f.uploadToken.Samantha.UploadInfo.SpaceName,
				"FileType":     "video",
				"IsInner":      "1",
				"NeedFallback": "true",
				"FileSize":     strconv.FormatInt(fileSize, 10),
				"s":            randomString(),
			}
		case ImgDataType, FileDataType:
			// 图片或其他文件类型 - 使用uploadToken.Alice对应配置
			uploadURL = "https://" + f.uploadToken.Alice[dataType].UploadHost
			params = map[string]string{
				"Action":        "ApplyImageUpload",
				"Version":       "2018-08-01",
				"ServiceId":     f.uploadToken.Alice[dataType].ServiceID,
				"NeedFallback":  "true",
				"FileSize":      strconv.FormatInt(fileSize, 10),
				"FileExtension": stdpath.Ext(fileName),
				"s":             randomString(),
			}
		}
		return uploadURL, params
	}
	// 获取初始参数
	uploadURL, params := configureParams()
	tokenRefreshed := false
	var configResp api.UploadConfigResp
	paramsValues := url.Values{}
	for key, value := range params {
		paramsValues.Add(key, value)
	}

	extraHeaders := map[string]string{
		"user-agent": defaultUserAgent,
	}

	opts := rest.Opts{
		Method:       "GET",
		RootURL:      uploadURL,
		Parameters:   paramsValues,
		ExtraHeaders: extraHeaders,
	}

	_, err = f.requestAPI(ctx, opts, tokenType, &configResp, nil)

	if err != nil {
		return err
	}

	if configResp.ResponseMetadata.Error.Code == "" {
		*config = configResp.Result
		return nil
	}

	// 100028 凭证过期
	if configResp.ResponseMetadata.Error.CodeN == 100028 && !tokenRefreshed {
		newToken, err := f.initUploadToken(ctx)
		if err != nil {
			return fmt.Errorf("failed to refresh token: %w", err)
		}
		f.uploadToken = newToken
		// tokenRefreshed = true
		// uploadURL, params = configureParams()

		return errors.New("token refreshed, retry needed")
	}

	return fmt.Errorf("get upload_config failed: %s", configResp.ResponseMetadata.Error.Message)

}

func (f *Fs) requestAPI(ctx context.Context, opts rest.Opts, tokenType string, resp interface{}, callback api.ReqCallback) ([]byte, error) {

	var err error

	// extraHeaders := map[string]string{
	// 	"user-agent": defaultUserAgent,
	// }
	// if method == http.MethodPost {
	// 	extraHeaders["Content-Type"] = "text/plain;charset=UTF-8"
	// }

	// opts := rest.Opts{
	// 	Method:       method,
	// 	RootURL:      url,
	// 	Parameters:   paramegers,
	// 	ExtraHeaders: extraHeaders,
	// }

	// if req != nil {
	// 	// 序列化 struct 为 JSON 字节流
	// 	data, err := json.Marshal(req)
	// 	if err != nil {
	// 		return nil, err
	// 	}
	// 	// 使用 bytes.Buffer 创建一个 io.Reader
	// 	reader := bytes.NewReader(data)
	// 	opts.Body = reader
	// }

	if callback != nil {
		callback(&opts)
	}

	requrl := fmt.Sprintf("%s%s", opts.RootURL, opts.Path)

	// 使用自定义AWS SigV4签名
	err = f.signRequest(&opts, opts.Method, tokenType, requrl)
	if err != nil {
		return nil, err
	}

	var apiResp *http.Response
	err = f.pacer.Call(func() (bool, error) {
		apiResp, err = f.rst.Call(ctx, &opts)
		if err != nil {
			time.Sleep(5 * time.Second)
			return true, errors.New("requestApi请求错误")
		}
		return f.shouldRetry(ctx, apiResp, err)
	})
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(apiResp.Body)
	if err != nil {
		return nil, err
	}

	if resp != nil {
		err = json.Unmarshal(data, &resp)
		if err != nil {
			fs.Fatalf("格式化响应到实例错误: %v", err.Error())
			return nil, err
		}
	}

	return data, nil

}

func (f *Fs) signRequest(req *rest.Opts, method, tokenType, uploadURL string) error {
	parsedURL, err := url.Parse(uploadURL)
	if err != nil {
		return fmt.Errorf("invalid URL format: %w", err)
	}

	var accessKeyID, secretAccessKey, sessionToken string
	var serviceName string

	if tokenType == VideoDataType {
		accessKeyID = f.uploadToken.Samantha.StsToken.AccessKeyID
		secretAccessKey = f.uploadToken.Samantha.StsToken.SecretAccessKey
		sessionToken = f.uploadToken.Samantha.StsToken.SessionToken
		serviceName = "vod"
	} else {
		accessKeyID = f.uploadToken.Alice[tokenType].Auth.AccessKeyID
		secretAccessKey = f.uploadToken.Alice[tokenType].Auth.SecretAccessKey
		sessionToken = f.uploadToken.Alice[tokenType].Auth.SessionToken
		serviceName = "imagex"
	}

	// 当前时间，格式为 ISO8601
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.ExtraHeaders["X-Amz-Date"] = amzDate

	if sessionToken != "" {
		req.ExtraHeaders["X-Amz-Security-Token"] = sessionToken
	}

	// 计算请求体的SHA256哈希
	var bodyHash string
	if req.Body != nil {
		// 将 io.ReadCloser 转换为 []byte
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return fmt.Errorf("request body must be []byte")
		}
		bodyHash = hashSHA256(string(bodyBytes))
		req.ExtraHeaders["X-Amz-Content-Sha256"] = bodyHash
		req.Body = bytes.NewReader(bodyBytes) //把Body还回去
	} else {
		bodyHash = hashSHA256("")
	}
	// 创建规范请求
	canonicalURI := parsedURL.Path
	if canonicalURI == "" {
		canonicalURI = "/"
	}

	// 使用 url.Values 转换成 map[string][]string
	headersMap := url.Values{}
	for key, value := range req.ExtraHeaders {
		headersMap.Add(key, value)
	}

	// 查询参数按照字母顺序排序
	canonicalQueryString := getCanonicalQueryString(req.Parameters)

	// 规范请求头
	canonicalHeaders, signedHeaders := getCanonicalHeadersFromMap(headersMap)
	canonicalRequest := method + "\n" +
		canonicalURI + "\n" +
		canonicalQueryString + "\n" +
		canonicalHeaders + "\n" +
		signedHeaders + "\n" +
		bodyHash

	algorithm := "AWS4-HMAC-SHA256"
	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, Region, serviceName)

	stringToSign := algorithm + "\n" +
		amzDate + "\n" +
		credentialScope + "\n" +
		hashSHA256(canonicalRequest)
	// 计算签名密钥
	signingKey := getSigningKey(secretAccessKey, dateStamp, Region, serviceName)
	// 计算签名
	signature := hmacSHA256Hex(signingKey, stringToSign)
	// 构建授权头
	authorizationHeader := fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm,
		accessKeyID,
		credentialScope,
		signedHeaders,
		signature,
	)

	req.ExtraHeaders["Authorization"] = authorizationHeader
	//return errors.New("测试签名")
	return nil
}

// 获取规范查询字符串
func getCanonicalQueryString(query url.Values) string {
	if len(query) == 0 {
		return ""
	}

	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		values := query[k]
		for _, v := range values {
			parts = append(parts, urlEncode(k)+"="+urlEncode(v))
		}
	}

	return strings.Join(parts, "&")
}
func urlEncode(s string) string {
	s = url.QueryEscape(s)
	s = strings.ReplaceAll(s, "+", "%20")
	return s
}

// 获取规范头信息和已签名头列表
func getCanonicalHeadersFromMap(headers map[string][]string) (string, string) {
	// 不可签名的头部列表
	unsignableHeaders := map[string]bool{
		"authorization":     true,
		"content-type":      true,
		"content-length":    true,
		"user-agent":        true,
		"presigned-expires": true,
		"expect":            true,
		"x-amzn-trace-id":   true,
	}
	headerValues := make(map[string]string)
	var signedHeadersList []string

	for k, v := range headers {
		if len(v) == 0 {
			continue
		}

		lowerKey := strings.ToLower(k)
		// 检查是否可签名
		if strings.HasPrefix(lowerKey, "x-amz-") || !unsignableHeaders[lowerKey] {
			value := strings.TrimSpace(v[0])
			value = strings.Join(strings.Fields(value), " ")
			headerValues[lowerKey] = value
			signedHeadersList = append(signedHeadersList, lowerKey)
		}
	}

	sort.Strings(signedHeadersList)

	var canonicalHeadersStr strings.Builder
	for _, key := range signedHeadersList {
		canonicalHeadersStr.WriteString(key)
		canonicalHeadersStr.WriteString(":")
		canonicalHeadersStr.WriteString(headerValues[key])
		canonicalHeadersStr.WriteString("\n")
	}

	signedHeaders := strings.Join(signedHeadersList, ";")

	return canonicalHeadersStr.String(), signedHeaders
}

// 计算HMAC-SHA256
func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// 计算HMAC-SHA256并返回十六进制字符串
func hmacSHA256Hex(key []byte, data string) string {
	return hex.EncodeToString(hmacSHA256(key, data))
}

// 计算SHA256哈希并返回十六进制字符串
func hashSHA256(data string) string {
	h := sha256.New()
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// 获取签名密钥
func getSigningKey(secretKey, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	return kSigning
}

func (f *Fs) getFile(ctx context.Context, o *Object) (file *api.File, err error) {
	switch f.opt.DownloadAPI {
	case "get_download_info":
		opts := rest.Opts{
			Method: "POST",
			Path:   "/samantha/aispace/get_download_info",
		}
		// req := map[string]string{
		//  "requests":  opt.Username,
		//  "password":  pass,
		//  "client_id": clientID,
		// }
		req := api.FileLinkRequest{
			Requests: []api.NodeID{
				{NodeID: o.id},
			},
		}

		var resp *http.Response
		var info api.GetDownloadInfoResp
		err = f.pacer.Call(func() (bool, error) {
			resp, err = f.rst.CallJSON(ctx, &opts, &req, &info)
			if err != nil {
				time.Sleep(5 * time.Second)
				return true, errors.New("get_download_info没有找到文件下载链接")
			}
			return f.shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return nil, err
		}
		var downloadURL string
		if len(info.Data.DownloadInfos) > 0 {
			downloadURL = info.Data.DownloadInfos[0].MainURL
		} else {
			return nil, errors.New("没有获取到下载链接")
		}
		// 获取当前时间
		currentTime := time.Now()
		// 计算失效时间：当前时间加上有效期（分钟）
		expireTime := currentTime.Add(time.Duration(f.opt.DurationInMinutes) * time.Minute)
		o.file.Link = &api.Link{URL: fmt.Sprintf("%s&expireTime=%d", downloadURL, expireTime.Unix()), Expire: api.Time(expireTime)}
		return o.file, nil
	case "get_file_url":
		switch o.file.NodeType {
		case VideoType, AudioType:
			var info api.GetVideoFileURLResp
			var resp *http.Response
			opts := rest.Opts{
				Method: "POST",
				Path:   "/samantha/media/get_play_info",
			}

			req := api.GetVideoFileURLRequest{
				Key:    o.file.Key,
				NodeID: o.id,
			}

			err = f.pacer.Call(func() (bool, error) {
				resp, err = f.rst.CallJSON(ctx, &opts, &req, &info)
				if err != nil {
					time.Sleep(5 * time.Second)
					return true, errors.New("get_play_info没有找到文件下载链接")
				}
				return f.shouldRetry(ctx, resp, err)
			})
			if err != nil {
				return nil, err
			}
			var downloadURL string
			downloadURL = info.Data.OriginalMediaInfo.MainURL

			// 获取当前时间
			currentTime := time.Now()
			// 计算失效时间：当前时间加上有效期（分钟）
			expireTime := currentTime.Add(time.Duration(f.opt.DurationInMinutes) * time.Minute)
			o.file.Link = &api.Link{URL: fmt.Sprintf("%s&expireTime=%d", downloadURL, expireTime.Unix()), Expire: api.Time(expireTime)}
			return o.file, nil

		default:

			var info api.GetFileURLResp
			var resp *http.Response
			opts := rest.Opts{
				Method: "POST",
				Path:   "/alice/message/get_file_url",
			}

			req := api.GetFileURLRequest{
				Uris: []string{o.file.Key},
				Type: FileNodeType[o.file.NodeType],
			}
			err = f.pacer.Call(func() (bool, error) {
				resp, err = f.rst.CallJSON(ctx, &opts, &req, &info)
				if err != nil {
					time.Sleep(5 * time.Second)
					return true, errors.New("get_file_url没有找到文件下载链接")
				}
				return f.shouldRetry(ctx, resp, err)
			})
			if err != nil {
				return nil, err
			}
			var downloadURL string
			if len(info.Data.FileUrls) > 0 {
				downloadURL = info.Data.FileUrls[0].MainURL
			} else {
				return nil, errors.New("没有获取到下载链接")
			}
			// 获取当前时间
			currentTime := time.Now()
			// 计算失效时间：当前时间加上有效期（分钟）

			expireTime := currentTime.Add(time.Duration(f.opt.DurationInMinutes) * time.Minute)
			o.file.Link = &api.Link{URL: fmt.Sprintf("%s&expireTime=%d", downloadURL, expireTime.Unix()), Expire: api.Time(expireTime)}
			return o.file, nil
		}
	default:
		return nil, errors.New("没有获取到下载链接")
	}
}

// getTask gets api.Task from API for the ID passed
func (f *Fs) getTask(ctx context.Context, ID string, checkPhase bool) (info *api.Task, err error) {
	opts := rest.Opts{
		Method: "GET",
		Path:   "/drive/v1/tasks/" + ID,
	}
	var resp *http.Response
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.rst.CallJSON(ctx, &opts, nil, &info)
		if checkPhase {
			if err == nil && info.Phase != api.PhaseTypeComplete {
				// could be pending right after the task is created
				return true, fmt.Errorf("%s (%s) is still in %s", info.Name, info.Type, info.Phase)
			}
		}
		return f.shouldRetry(ctx, resp, err)
	})
	return
}

// waitTask waits for async tasks to be completed
func (f *Fs) waitTask(ctx context.Context, ID string) (err error) {
	time.Sleep(taskWaitTime)
	if info, err := f.getTask(ctx, ID, true); err != nil {
		if info == nil {
			return fmt.Errorf("can't verify the task is completed: %q", ID)
		}
		return fmt.Errorf("can't verify the task is completed: %#v", info)
	}
	return
}

// getAbout gets drive#quota information from server
func (f *Fs) getAbout(ctx context.Context) (info *api.About, err error) {
	opts := rest.Opts{
		Method: "GET",
		Path:   "/drive/v1/about",
	}
	var resp *http.Response
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.rst.CallJSON(ctx, &opts, nil, &info)
		return f.shouldRetry(ctx, resp, err)
	})
	return
}

// getGcid retrieves Gcid cached in API server
func (f *Fs) getGcid(ctx context.Context, src fs.ObjectInfo) (gcid string, err error) {
	cid, err := calcCid(ctx, src)
	if err != nil {
		return
	}
	if src.Size() == 0 {
		// If src is zero-length, the API will return
		// Error "cid and file_size is required" (400)
		// In this case, we can simply return cid == gcid
		return cid, nil
	}

	params := url.Values{}
	params.Set("cid", cid)
	params.Set("file_size", strconv.FormatInt(src.Size(), 10))
	opts := rest.Opts{
		Method:     "GET",
		Path:       "/drive/v1/resource/cid",
		Parameters: params,
	}

	info := struct {
		Gcid string `json:"gcid,omitempty"`
	}{}
	var resp *http.Response
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.rst.CallJSON(ctx, &opts, nil, &info)
		return f.shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return "", err
	}
	return info.Gcid, nil
}

// Read the gcid of in returning a reader which will read the same contents
//
// The cleanup function should be called when out is finished with
// regardless of whether this function returned an error or not.
func readGcid(in io.Reader, size, threshold int64) (gcid string, out io.Reader, cleanup func(), err error) {
	// nothing to clean up by default
	cleanup = func() {}

	// don't cache small files on disk to reduce wear of the disk
	if size > threshold {
		var tempFile *os.File

		// create the cache file
		tempFile, err = os.CreateTemp("", cachePrefix)
		if err != nil {
			return
		}

		_ = os.Remove(tempFile.Name()) // Delete the file - may not work on Windows

		// clean up the file after we are done downloading
		cleanup = func() {
			// the file should normally already be close, but just to make sure
			_ = tempFile.Close()
			_ = os.Remove(tempFile.Name()) // delete the cache file after we are done - may be deleted already
		}

		// use the teeReader to write to the local file AND calculate the gcid while doing so
		teeReader := io.TeeReader(in, tempFile)

		// copy the ENTIRE file to disk and calculate the gcid in the process
		if gcid, err = calcGcid(teeReader, size); err != nil {
			return
		}
		// jump to the start of the local file so we can pass it along
		if _, err = tempFile.Seek(0, 0); err != nil {
			return
		}

		// replace the already read source with a reader of our cached file
		out = tempFile
	} else {
		buf := &bytes.Buffer{}
		teeReader := io.TeeReader(in, buf)

		if gcid, err = calcGcid(teeReader, size); err != nil {
			return
		}
		out = buf
	}
	return
}

// calcGcid calculates Gcid from reader
//
// Gcid is a custom hash to index a file contents
func calcGcid(r io.Reader, size int64) (string, error) {
	calcBlockSize := func(j int64) int64 {
		var psize int64 = 0x40000
		for float64(j)/float64(psize) > 0x200 && psize < 0x200000 {
			psize <<= 1
		}
		return psize
	}

	totalHash := sha1.New()
	blockHash := sha1.New()
	readSize := calcBlockSize(size)
	for {
		blockHash.Reset()
		if n, err := io.CopyN(blockHash, r, readSize); err != nil && n == 0 {
			if err != io.EOF {
				return "", err
			}
			break
		}
		totalHash.Write(blockHash.Sum(nil))
	}
	return hex.EncodeToString(totalHash.Sum(nil)), nil
}

// unWrapObjectInfo returns the underlying Object unwrapped as much as
// possible or nil even if it is an OverrideRemote
func unWrapObjectInfo(oi fs.ObjectInfo) fs.Object {
	if o, ok := oi.(fs.Object); ok {
		return fs.UnWrapObject(o)
	} else if do, ok := oi.(*fs.OverrideRemote); ok {
		// Unwrap if it is an operations.OverrideRemote
		return do.UnWrap()
	}
	return nil
}

// calcCid calculates Cid from source
//
// Cid is a simplified version of Gcid
func calcCid(ctx context.Context, src fs.ObjectInfo) (cid string, err error) {
	srcObj := unWrapObjectInfo(src)
	if srcObj == nil {
		return "", fmt.Errorf("failed to unwrap object from src: %s", src)
	}

	size := src.Size()
	hash := sha1.New()
	var rc io.ReadCloser

	readHash := func(start, length int64) (err error) {
		end := start + length - 1
		if rc, err = srcObj.Open(ctx, &fs.RangeOption{Start: start, End: end}); err != nil {
			return fmt.Errorf("failed to open src with range (%d, %d): %w", start, end, err)
		}
		defer fs.CheckClose(rc, &err)
		_, err = io.Copy(hash, rc)
		return err
	}

	if size <= 0xF000 { // 61440 = 60KB
		err = readHash(0, size)
	} else { // 20KB from three different parts
		for _, start := range []int64{0, size / 3, size - 0x5000} {
			err = readHash(start, 0x5000)
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		return "", fmt.Errorf("failed to hash: %w", err)
	}
	cid = strings.ToUpper(hex.EncodeToString(hash.Sum(nil)))
	return
}

// ------------------------------------------------------------ authorization

// randomly generates device id used for request header 'x-device-id'
//
// original javascript implementation
//
//	return "xxxxxxxxxxxx4xxxyxxxxxxxxxxxxxxx".replace(/[xy]/g, (e) => {
//	    const t = (16 * Math.random()) | 0;
//	    return ("x" == e ? t : (3 & t) | 8).toString(16);
//	});
func genDeviceID() string {
	base := []byte("xxxxxxxxxxxx4xxxyxxxxxxxxxxxxxxx")
	for i, char := range base {
		switch char {
		case 'x':
			base[i] = fmt.Sprintf("%x", rand.Intn(16))[0]
		case 'y':
			base[i] = fmt.Sprintf("%x", rand.Intn(16)&3|8)[0]
		}
	}
	return string(base)
}

// pikpakClient wraps rest.Client with a handle of captcha token
type doubaoClient struct {
	opt    *Options
	client *rest.Client
}

// newPikpakClient takes an (oauth) http.Client and makes a new api instance for pikpak with
// * error handler
// * root url
// * default headers
func newDoubaoClient(c *http.Client, opt *Options) *doubaoClient {
	//client := rest.NewClient(c).SetErrorHandler(errorHandler).SetRoot("https://doubao.com")
	client := rest.NewClient(c).SetRoot("https://www.doubao.com")
	for key, val := range map[string]string{
		"Referer": "https://www.doubao.com",
		"Cookie":  opt.Cookie,
	} {
		client.SetHeader(key, val)
	}
	return &doubaoClient{
		client: client,
		opt:    opt,
	}

}

func (c *doubaoClient) CallJSON(ctx context.Context, opts *rest.Opts, request any, response any) (resp *http.Response, err error) {
	return c.client.CallJSON(ctx, opts, request, response)
}

func (c *doubaoClient) Call(ctx context.Context, opts *rest.Opts) (resp *http.Response, err error) {
	return c.client.Call(ctx, opts)
}

func randomString() string {
	const charset = "0123456789abcdefghijklmnopqrstuvwxyz"
	const length = 11 // 11位随机字符串

	var sb strings.Builder
	sb.Grow(length)

	for i := 0; i < length; i++ {
		sb.WriteByte(charset[rand.Intn(len(charset))])
	}

	return sb.String()
}
