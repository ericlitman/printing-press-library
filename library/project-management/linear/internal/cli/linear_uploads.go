package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mvanhorn/printing-press-library/library/project-management/linear/internal/client"
)

var uploadHTTPClient = &http.Client{Timeout: 60 * time.Second}

const maxLinearMediaUploadBytes int64 = 50 << 20

type uploadedAsset struct {
	Path        string `json:"path"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
	AssetURL    string `json:"asset_url"`
}

func uploadMediaFiles(c *client.Client, paths []string, makePublic bool) ([]uploadedAsset, error) {
	assets := make([]uploadedAsset, 0, len(paths))
	for _, path := range paths {
		asset, err := uploadMediaFile(c, path, makePublic)
		if err != nil {
			if len(assets) > 0 {
				return nil, fmt.Errorf("%w; %d media file(s) were already uploaded (%s)", err, len(assets), uploadedAssetURLSummary(assets))
			}
			return nil, err
		}
		assets = append(assets, asset)
	}
	return assets, nil
}

func mutationErrorAfterMediaUpload(operation string, err error, assets []uploadedAsset) error {
	if len(assets) == 0 {
		return fmt.Errorf("%s failed: %w", operation, err)
	}
	return fmt.Errorf("%s failed after uploading %d media file(s) (%s); manual cleanup may be needed: %w", operation, len(assets), uploadedAssetURLSummary(assets), err)
}

func uploadedAssetURLSummary(assets []uploadedAsset) string {
	if len(assets) == 0 {
		return "no asset URLs"
	}
	limit := len(assets)
	if limit > 3 {
		limit = 3
	}
	urls := make([]string, 0, limit+1)
	for i := 0; i < limit; i++ {
		url := assets[i].AssetURL
		if url == "" {
			url = assets[i].Filename
		}
		urls = append(urls, url)
	}
	if len(assets) > limit {
		urls = append(urls, fmt.Sprintf("and %d more", len(assets)-limit))
	}
	return strings.Join(urls, ", ")
}

func uploadMediaFile(c *client.Client, path string, makePublic bool) (uploadedAsset, error) {
	f, err := os.Open(path)
	if err != nil {
		return uploadedAsset{}, fmt.Errorf("opening media file %q: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return uploadedAsset{}, fmt.Errorf("stat media file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return uploadedAsset{}, fmt.Errorf("media file %q is not a regular file", path)
	}
	if info.Size() > maxLinearMediaUploadBytes {
		return uploadedAsset{}, fmt.Errorf("media file %q is %d bytes, exceeds %d byte limit", path, info.Size(), maxLinearMediaUploadBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxLinearMediaUploadBytes+1))
	if err != nil {
		return uploadedAsset{}, fmt.Errorf("reading media file %q: %w", path, err)
	}
	if int64(len(data)) > maxLinearMediaUploadBytes {
		return uploadedAsset{}, fmt.Errorf("media file %q exceeds %d byte limit while reading", path, maxLinearMediaUploadBytes)
	}
	filename := filepath.Base(path)
	contentType := detectMediaContentType(filename, data)

	const mutation = `mutation PrepareFileUpload($contentType: String!, $filename: String!, $size: Int!, $makePublic: Boolean) {
		fileUpload(contentType: $contentType, filename: $filename, size: $size, makePublic: $makePublic) {
			success
			uploadFile {
				assetUrl
				uploadUrl
				filename
				contentType
				size
				headers { key value }
			}
		}
	}`
	resp, err := c.Mutate(mutation, map[string]any{
		"contentType": contentType,
		"filename":    filename,
		"size":        len(data),
		"makePublic":  makePublic,
	})
	if err != nil {
		return uploadedAsset{}, fmt.Errorf("fileUpload failed for %q: %w", path, err)
	}

	var parsed struct {
		FileUpload struct {
			Success    bool `json:"success"`
			UploadFile *struct {
				AssetURL    string `json:"assetUrl"`
				UploadURL   string `json:"uploadUrl"`
				Filename    string `json:"filename"`
				ContentType string `json:"contentType"`
				Size        int    `json:"size"`
				Headers     []struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				} `json:"headers"`
			} `json:"uploadFile"`
		} `json:"fileUpload"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		return uploadedAsset{}, fmt.Errorf("parsing fileUpload response for %q: %w", path, err)
	}
	if !parsed.FileUpload.Success || parsed.FileUpload.UploadFile == nil {
		return uploadedAsset{}, fmt.Errorf("Linear reported fileUpload(%s) success=false", path)
	}
	up := parsed.FileUpload.UploadFile
	if up.ContentType != "" {
		contentType = up.ContentType
	}
	if up.Filename != "" {
		filename = up.Filename
	}

	req, err := http.NewRequest(http.MethodPut, up.UploadURL, bytes.NewReader(data))
	if err != nil {
		return uploadedAsset{}, fmt.Errorf("creating upload request for %q: %w", path, err)
	}
	for _, h := range up.Headers {
		req.Header.Set(h.Key, h.Value)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.ContentLength = int64(len(data))

	respHTTP, err := uploadHTTPClient.Do(req)
	if err != nil {
		return uploadedAsset{}, fmt.Errorf("uploading %q: %w", path, err)
	}
	defer respHTTP.Body.Close()
	if respHTTP.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(respHTTP.Body, 4096))
		return uploadedAsset{}, fmt.Errorf("uploading %q returned HTTP %d: %s", path, respHTTP.StatusCode, strings.TrimSpace(string(body)))
	}

	return uploadedAsset{
		Path:        path,
		Filename:    filename,
		ContentType: contentType,
		Size:        up.Size,
		AssetURL:    up.AssetURL,
	}, nil
}

func detectMediaContentType(filename string, data []byte) string {
	if ct := mime.TypeByExtension(filepath.Ext(filename)); ct != "" {
		return ct
	}
	if len(data) > 0 {
		return http.DetectContentType(data)
	}
	return "application/octet-stream"
}

func appendMediaMarkdown(body string, assets []uploadedAsset) string {
	if len(assets) == 0 {
		return body
	}
	var b strings.Builder
	b.WriteString(body)
	if strings.TrimSpace(body) != "" {
		b.WriteString("\n\n")
	}
	for _, asset := range assets {
		label := escapeMarkdownLinkText(asset.Filename)
		if strings.HasPrefix(asset.ContentType, "image/") {
			fmt.Fprintf(&b, "![%s](%s)\n", label, asset.AssetURL)
		} else {
			fmt.Fprintf(&b, "[%s](%s)\n", label, asset.AssetURL)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func escapeMarkdownLinkText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '[', ']', '(', ')':
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
