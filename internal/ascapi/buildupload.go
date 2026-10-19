package ascapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// App 是 GET /v1/apps 返回的一条 App 记录。
type App struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	BundleID string `json:"bundleId"`
}

// ListApps 列出当前密钥可访问的全部 App（limit=200，足够常见团队）。
func ListApps(ctx context.Context, c *Client) ([]App, error) {
	q := url.Values{}
	q.Set("limit", "200")
	q.Set("sort", "name")
	q.Set("fields[apps]", "name,bundleId")
	var doc struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Name     string `json:"name"`
				BundleID string `json:"bundleId"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := c.do(ctx, "GET", "/v1/apps?"+q.Encode(), nil, &doc); err != nil {
		return nil, err
	}
	apps := make([]App, 0, len(doc.Data))
	for _, d := range doc.Data {
		apps = append(apps, App{ID: d.ID, Name: d.Attributes.Name, BundleID: d.Attributes.BundleID})
	}
	return apps, nil
}

// HTTPHeader / UploadOperation 是 buildUploadFiles 下发的分片上传操作。
type HTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type UploadOperation struct {
	Method         string       `json:"method"`
	URL            string       `json:"url"`
	Length         int64        `json:"length"`
	Offset         int64        `json:"offset"`
	PartNumber     int          `json:"partNumber"`
	RequestHeaders []HTTPHeader `json:"requestHeaders"`
}

// StateDetail 是状态对象里的 errors/warnings/infos 条目。
type StateDetail struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// UploadState 是 buildUpload 的 state 字段——可能为字符串
// （"AWAITING_UPLOAD"）或对象（{"state":"FAILED","errors":[...]}），两种都兼容。
type UploadState struct {
	State    string        `json:"state"`
	Errors   []StateDetail `json:"errors"`
	Warnings []StateDetail `json:"warnings"`
	Infos    []StateDetail `json:"infos"`
}

func parseState(raw json.RawMessage) UploadState {
	var st UploadState
	if len(raw) == 0 {
		return st
	}
	if err := json.Unmarshal(raw, &st.State); err == nil {
		return st
	}
	_ = json.Unmarshal(raw, &st)
	return st
}

// BuildUpload 是 GET /v1/buildUploads/{id} 的解析结果。
type BuildUpload struct {
	ID                   string
	State                UploadState
	Version              string
	Build                string
	Platform             string
	BuildID              string // 处理完成后关联的 Build 资源 ID（可能为空）
	BuildVersion         string // included build 的版本
	BuildProcessingState string // included build 的处理状态
}

type buildUploadDoc struct {
	Data struct {
		ID         string `json:"id"`
		Attributes struct {
			Platform                   string          `json:"platform"`
			CFBundleShortVersionString string          `json:"cfBundleShortVersionString"`
			CFBundleVersion            string          `json:"cfBundleVersion"`
			CreatedDate                string          `json:"createdDate"`
			UploadedDate               string          `json:"uploadedDate"`
			State                      json.RawMessage `json:"state"`
		} `json:"attributes"`
		Relationships struct {
			Build *struct {
				Data *struct {
					ID string `json:"id"`
				} `json:"data"`
			} `json:"build"`
		} `json:"relationships"`
	} `json:"data"`
	Included []struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Attributes struct {
			Version         string `json:"version"`
			ProcessingState string `json:"processingState"`
		} `json:"attributes"`
	} `json:"included"`
}

func (d *buildUploadDoc) toBuildUpload() *BuildUpload {
	bu := &BuildUpload{
		ID:       d.Data.ID,
		State:    parseState(d.Data.Attributes.State),
		Version:  d.Data.Attributes.CFBundleShortVersionString,
		Build:    d.Data.Attributes.CFBundleVersion,
		Platform: d.Data.Attributes.Platform,
	}
	if d.Data.Relationships.Build != nil && d.Data.Relationships.Build.Data != nil {
		bu.BuildID = d.Data.Relationships.Build.Data.ID
		for _, inc := range d.Included {
			if inc.ID == bu.BuildID {
				bu.BuildVersion = inc.Attributes.Version
				bu.BuildProcessingState = inc.Attributes.ProcessingState
			}
		}
	}
	return bu
}

// CreateBuildUpload 创建上传会话（官方流程第 1 步）。
func CreateBuildUpload(ctx context.Context, c *Client, appID, version, build, platform string) (string, error) {
	body := map[string]any{
		"data": map[string]any{
			"type": "buildUploads",
			"attributes": map[string]any{
				"cfBundleShortVersionString": version,
				"cfBundleVersion":            build,
				"platform":                   platform,
			},
			"relationships": map[string]any{
				"app": map[string]any{"data": map[string]any{"type": "apps", "id": appID}},
			},
		},
	}
	var doc struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, "POST", "/v1/buildUploads", body, &doc); err != nil {
		return "", err
	}
	if doc.Data.ID == "" {
		return "", fmt.Errorf("创建上传会话失败：响应中没有 id")
	}
	return doc.Data.ID, nil
}

// BuildUploadFile 是预留的文件位及其分片上传操作。
type BuildUploadFile struct {
	ID       string
	FileName string
	FileSize int64
	Ops      []UploadOperation
}

// ReserveFile 为安装包预留上传位（官方流程第 2 步）。
func ReserveFile(ctx context.Context, c *Client, buildUploadID, fileName string, fileSize int64, uti string) (*BuildUploadFile, error) {
	body := map[string]any{
		"data": map[string]any{
			"type": "buildUploadFiles",
			"attributes": map[string]any{
				"assetType": "ASSET",
				"fileName":  fileName,
				"fileSize":  fileSize,
				"uti":       uti,
			},
			"relationships": map[string]any{
				"buildUpload": map[string]any{"data": map[string]any{"type": "buildUploads", "id": buildUploadID}},
			},
		},
	}
	var doc struct {
		Data struct {
			ID         string `json:"id"`
			Attributes struct {
				FileName         string            `json:"fileName"`
				FileSize         int64             `json:"fileSize"`
				UploadOperations []UploadOperation `json:"uploadOperations"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := c.do(ctx, "POST", "/v1/buildUploadFiles", body, &doc); err != nil {
		return nil, err
	}
	if doc.Data.ID == "" {
		return nil, fmt.Errorf("预留文件位失败：响应中没有 id")
	}
	return &BuildUploadFile{ID: doc.Data.ID, FileName: doc.Data.Attributes.FileName, FileSize: doc.Data.Attributes.FileSize, Ops: doc.Data.Attributes.UploadOperations}, nil
}

// CommitFile 提交文件并附 MD5 校验和（官方流程第 4 步）。
func CommitFile(ctx context.Context, c *Client, fileID, md5Hex string) error {
	body := map[string]any{
		"data": map[string]any{
			"type": "buildUploadFiles",
			"id":   fileID,
			"attributes": map[string]any{
				"uploaded": true,
				"sourceFileChecksums": map[string]any{
					"file": map[string]any{"algorithm": "MD5", "hash": md5Hex},
				},
			},
		},
	}
	return c.do(ctx, "PATCH", "/v1/buildUploadFiles/"+fileID, body, nil)
}

// GetBuildUpload 查询上传会话状态（官方流程第 5 步，include=build）。
func GetBuildUpload(ctx context.Context, c *Client, id string) (*BuildUpload, error) {
	var doc buildUploadDoc
	if err := c.do(ctx, "GET", "/v1/buildUploads/"+id+"?include=build", nil, &doc); err != nil {
		return nil, err
	}
	return doc.toBuildUpload(), nil
}

// ListBuildUploads 列出某 App 的历史上传会话。
func ListBuildUploads(ctx context.Context, c *Client, appID string, limit int) ([]*BuildUpload, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	q := url.Values{}
	q.Set("limit", fmt.Sprint(limit))
	var doc struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Platform                   string          `json:"platform"`
				CFBundleShortVersionString string          `json:"cfBundleShortVersionString"`
				CFBundleVersion            string          `json:"cfBundleVersion"`
				CreatedDate                string          `json:"createdDate"`
				State                      json.RawMessage `json:"state"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := c.do(ctx, "GET", "/v1/apps/"+appID+"/buildUploads?"+q.Encode(), nil, &doc); err != nil {
		return nil, err
	}
	out := make([]*BuildUpload, 0, len(doc.Data))
	for _, d := range doc.Data {
		var raw json.RawMessage = d.Attributes.State
		out = append(out, &BuildUpload{
			ID:       d.ID,
			State:    parseState(raw),
			Version:  d.Attributes.CFBundleShortVersionString,
			Build:    d.Attributes.CFBundleVersion,
			Platform: d.Attributes.Platform,
		})
	}
	return out, nil
}
