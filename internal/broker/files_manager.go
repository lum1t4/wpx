package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
)

// FileChunkBytes leaves ample room for base64 and the broker envelope under
// the two MiB message limit. Both sides reject larger decoded chunks.
const FileChunkBytes = 512 << 10

const (
	OpFileSearch      Operation = "file.search"
	OpFileDownload    Operation = "file.download"
	OpFileUploadChunk Operation = "file.upload_chunk"
	OpFileDelete      Operation = "file.delete"
	OpFileRename      Operation = "file.rename"
	OpFileArchive     Operation = "file.archive"
	OpFileExtract     Operation = "file.extract"
	OpFileCopy        Operation = "file.copy"
	OpFileMove        Operation = "file.move"
	OpFileMkdir       Operation = "file.mkdir"
)

type FileSearchRequest struct {
	Site  model.Site `json:"site"`
	Path  string     `json:"path"`
	Query string     `json:"query"`
	Limit int        `json:"limit"`
}

type FileSearchResult struct {
	Entries   []FileEntry `json:"entries"`
	Truncated bool        `json:"truncated,omitempty"`
}

type FileDownloadRequest struct {
	Site   model.Site `json:"site"`
	Path   string     `json:"path"`
	Offset int64      `json:"offset"`
	Limit  int        `json:"limit"`
}

type FileDownloadResult struct {
	Data   []byte `json:"data"`
	Offset int64  `json:"offset"`
	Next   int64  `json:"next_offset"`
	Size   int64  `json:"size"`
	EOF    bool   `json:"eof"`
}

type FileUploadChunkRequest struct {
	Site      model.Site `json:"site"`
	Path      string     `json:"path"`
	UploadID  string     `json:"upload_id"`
	Offset    int64      `json:"offset"`
	Data      []byte     `json:"data"`
	Final     bool       `json:"final"`
	Overwrite bool       `json:"overwrite"`
}

type FileUploadChunkResult struct {
	Next     int64 `json:"next_offset"`
	Complete bool  `json:"complete"`
}

type FilePathsRequest struct {
	Site  model.Site `json:"site"`
	Paths []string   `json:"paths"`
}

type FileRenameRequest struct {
	Site    model.Site `json:"site"`
	Path    string     `json:"path"`
	NewName string     `json:"new_name"`
}

type FileMkdirRequest struct {
	Site model.Site `json:"site"`
	Path string     `json:"path"`
}

type FileArchiveRequest struct {
	Site        model.Site `json:"site"`
	Paths       []string   `json:"paths"`
	Destination string     `json:"destination"`
}

type FileExtractRequest struct {
	Site        model.Site `json:"site"`
	Path        string     `json:"path"`
	Destination string     `json:"destination"`
}

type FileTransferRequest struct {
	Site        model.Site `json:"site"`
	Paths       []string   `json:"paths"`
	Destination string     `json:"destination"`
	Overwrite   bool       `json:"overwrite"`
}

type FileMutationResult struct {
	Changed int `json:"changed"`
}

// FileManagerOperator is deliberately optional so the original editor-only
// FileOperator remains source-compatible with tests and alternate backends.
type FileManagerOperator interface {
	SearchFiles(context.Context, model.Site, string, string, int) (FileSearchResult, error)
	DownloadFile(context.Context, model.Site, string, int64, int) (FileDownloadResult, error)
	UploadFileChunk(context.Context, model.Site, string, string, int64, []byte, bool, bool) (FileUploadChunkResult, error)
	DeleteFiles(context.Context, model.Site, []string) (int, error)
	RenameFile(context.Context, model.Site, string, string) error
	ArchiveFiles(context.Context, model.Site, []string, string) error
	ExtractFile(context.Context, model.Site, string, string) (int, error)
	CopyFiles(context.Context, model.Site, []string, string, bool) (int, error)
	MoveFiles(context.Context, model.Site, []string, string, bool) (int, error)
	CreateDirectory(context.Context, model.Site, string) error
}

var uploadIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func validateManagedSite(site model.Site) bool {
	return model.ValidateSite(site) == nil && site.Status == "active"
}

func validBrokerFilePath(path string, allowRoot bool) bool {
	if path == "" {
		return allowRoot
	}
	if strings.ContainsRune(path, 0) || strings.Contains(path, `\`) || filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	if clean == "." {
		return allowRoot
	}
	return clean == path && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func validFilePaths(paths []string) bool {
	if len(paths) == 0 || len(paths) > 100 {
		return false
	}
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if !validBrokerFilePath(path, false) {
			return false
		}
		if _, ok := seen[path]; ok {
			return false
		}
		seen[path] = struct{}{}
	}
	for parent := range seen {
		for child := range seen {
			if parent != child && strings.HasPrefix(child, parent+"/") {
				return false
			}
		}
	}
	return true
}

func decodeFileManagerPayload(raw json.RawMessage, dst any) bool {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(dst) != nil {
		return false
	}
	return decoder.Decode(&struct{}{}) == io.EOF
}

// dispatchFileManager is called by the central dispatcher before its switch.
// Returning handled=false lets existing file editor operations continue there.
func (s *Server) dispatchFileManager(request Request) (Response, bool) {
	response := Response{Version: ProtocolVersion, ID: request.ID}
	op, ok := s.Files.(FileManagerOperator)
	if !ok {
		switch request.Operation {
		case OpFileSearch, OpFileDownload, OpFileUploadChunk, OpFileDelete, OpFileRename, OpFileArchive, OpFileExtract, OpFileCopy, OpFileMove, OpFileMkdir:
			response.Error = "file manager operations are unavailable"
			return response, true
		default:
			return response, false
		}
	}
	ctx := context.Background()
	switch request.Operation {
	case OpFileSearch:
		var p FileSearchRequest
		if !decodeFileManagerPayload(request.Payload, &p) || !validateManagedSite(p.Site) || !validBrokerFilePath(p.Path, true) || len(strings.TrimSpace(p.Query)) < 1 || len(p.Query) > 200 || p.Limit < 1 || p.Limit > 500 {
			response.Error = "invalid file-search request"
			return response, true
		}
		result, err := op.SearchFiles(ctx, p.Site, p.Path, p.Query, p.Limit)
		return fileManagerResult(response, result, err, "search files"), true
	case OpFileDownload:
		var p FileDownloadRequest
		if !decodeFileManagerPayload(request.Payload, &p) || !validateManagedSite(p.Site) || !validBrokerFilePath(p.Path, false) || p.Offset < 0 || p.Limit < 1 || p.Limit > FileChunkBytes {
			response.Error = "invalid file-download request"
			return response, true
		}
		result, err := op.DownloadFile(ctx, p.Site, p.Path, p.Offset, p.Limit)
		return fileManagerResult(response, result, err, "download file"), true
	case OpFileUploadChunk:
		var p FileUploadChunkRequest
		if !decodeFileManagerPayload(request.Payload, &p) || !validateManagedSite(p.Site) || !validBrokerFilePath(p.Path, false) || !uploadIDPattern.MatchString(p.UploadID) || p.Offset < 0 || len(p.Data) > FileChunkBytes || (!p.Final && len(p.Data) == 0) {
			response.Error = "invalid file-upload request"
			return response, true
		}
		result, err := op.UploadFileChunk(ctx, p.Site, p.Path, p.UploadID, p.Offset, p.Data, p.Final, p.Overwrite)
		return fileManagerResult(response, result, err, "upload file"), true
	case OpFileDelete:
		var p FilePathsRequest
		if !decodeFileManagerPayload(request.Payload, &p) || !validateManagedSite(p.Site) || !validFilePaths(p.Paths) {
			response.Error = "invalid file-delete request"
			return response, true
		}
		changed, err := op.DeleteFiles(ctx, p.Site, p.Paths)
		return fileManagerResult(response, FileMutationResult{Changed: changed}, err, "delete files"), true
	case OpFileRename:
		var p FileRenameRequest
		if !decodeFileManagerPayload(request.Payload, &p) || !validateManagedSite(p.Site) || !validBrokerFilePath(p.Path, false) || !validFileBaseName(p.NewName) {
			response.Error = "invalid file-rename request"
			return response, true
		}
		err := op.RenameFile(ctx, p.Site, p.Path, p.NewName)
		return fileManagerResult(response, FileMutationResult{Changed: 1}, err, "rename file"), true
	case OpFileMkdir:
		var p FileMkdirRequest
		if !decodeFileManagerPayload(request.Payload, &p) || !validateManagedSite(p.Site) || !validBrokerFilePath(p.Path, false) {
			response.Error = "invalid directory-create request"
			return response, true
		}
		err := op.CreateDirectory(ctx, p.Site, p.Path)
		return fileManagerResult(response, FileMutationResult{Changed: 1}, err, "create directory"), true
	case OpFileArchive:
		var p FileArchiveRequest
		if !decodeFileManagerPayload(request.Payload, &p) || !validateManagedSite(p.Site) || !validFilePaths(p.Paths) || !validBrokerFilePath(p.Destination, false) || !strings.HasSuffix(strings.ToLower(p.Destination), ".zip") {
			response.Error = "invalid file-archive request"
			return response, true
		}
		err := op.ArchiveFiles(ctx, p.Site, p.Paths, p.Destination)
		return fileManagerResult(response, FileMutationResult{Changed: 1}, err, "archive files"), true
	case OpFileExtract:
		var p FileExtractRequest
		if !decodeFileManagerPayload(request.Payload, &p) || !validateManagedSite(p.Site) || !validBrokerFilePath(p.Path, false) || !validBrokerFilePath(p.Destination, true) {
			response.Error = "invalid file-extract request"
			return response, true
		}
		changed, err := op.ExtractFile(ctx, p.Site, p.Path, p.Destination)
		return fileManagerResult(response, FileMutationResult{Changed: changed}, err, "extract archive"), true
	case OpFileCopy, OpFileMove:
		var p FileTransferRequest
		if !decodeFileManagerPayload(request.Payload, &p) || !validateManagedSite(p.Site) || !validFilePaths(p.Paths) || !validBrokerFilePath(p.Destination, true) {
			response.Error = "invalid file-transfer request"
			return response, true
		}
		var changed int
		var err error
		if request.Operation == OpFileCopy {
			changed, err = op.CopyFiles(ctx, p.Site, p.Paths, p.Destination, p.Overwrite)
		} else {
			changed, err = op.MoveFiles(ctx, p.Site, p.Paths, p.Destination, p.Overwrite)
		}
		return fileManagerResult(response, FileMutationResult{Changed: changed}, err, "transfer files"), true
	default:
		return response, false
	}
}

func validFileBaseName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && filepath.Base(name) == name && !strings.ContainsAny(name, `/\`) && !strings.ContainsRune(name, 0)
}

func fileManagerResult(response Response, result any, err error, action string) Response {
	if err != nil {
		response.Error = action + ": " + err.Error()
		return response
	}
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		response.Error = fmt.Sprintf("%s: encode result", action)
		return response
	}
	response.OK = true
	response.Result = encoded
	return response
}

func (c Client) ListFiles(ctx context.Context, site model.Site, path string) ([]FileEntry, error) {
	var result FileListResult
	err := c.Call(ctx, OpFileList, "file.list:"+site.ID+":"+requestID(), FileRequest{Site: site, Path: path}, &result)
	return result.Entries, err
}

func (c Client) SearchFiles(ctx context.Context, site model.Site, path, query string, limit int) (FileSearchResult, error) {
	var result FileSearchResult
	err := c.Call(ctx, OpFileSearch, "file.search:"+site.ID+":"+requestID(), FileSearchRequest{Site: site, Path: path, Query: query, Limit: limit}, &result)
	return result, err
}

func (c Client) DownloadFile(ctx context.Context, site model.Site, path string, offset int64, limit int) (FileDownloadResult, error) {
	var result FileDownloadResult
	err := c.Call(ctx, OpFileDownload, "file.download:"+site.ID+":"+requestID(), FileDownloadRequest{Site: site, Path: path, Offset: offset, Limit: limit}, &result)
	return result, err
}

func (c Client) UploadFileChunk(ctx context.Context, request FileUploadChunkRequest) (FileUploadChunkResult, error) {
	var result FileUploadChunkResult
	key := fmt.Sprintf("file.upload:%s:%s:%d", request.Site.ID, request.UploadID, request.Offset)
	err := c.Call(ctx, OpFileUploadChunk, key, request, &result)
	return result, err
}

func (c Client) DeleteFiles(ctx context.Context, site model.Site, paths []string) (FileMutationResult, error) {
	return c.fileMutation(ctx, OpFileDelete, site, FilePathsRequest{Site: site, Paths: paths})
}

func (c Client) RenameFile(ctx context.Context, site model.Site, path, newName string) (FileMutationResult, error) {
	return c.fileMutation(ctx, OpFileRename, site, FileRenameRequest{Site: site, Path: path, NewName: newName})
}

func (c Client) CreateDirectory(ctx context.Context, site model.Site, path string) (FileMutationResult, error) {
	return c.fileMutation(ctx, OpFileMkdir, site, FileMkdirRequest{Site: site, Path: path})
}

func (c Client) ArchiveFiles(ctx context.Context, site model.Site, paths []string, destination string) (FileMutationResult, error) {
	return c.fileMutation(ctx, OpFileArchive, site, FileArchiveRequest{Site: site, Paths: paths, Destination: destination})
}

func (c Client) ExtractFile(ctx context.Context, site model.Site, path, destination string) (FileMutationResult, error) {
	return c.fileMutation(ctx, OpFileExtract, site, FileExtractRequest{Site: site, Path: path, Destination: destination})
}

func (c Client) CopyFiles(ctx context.Context, site model.Site, paths []string, destination string, overwrite bool) (FileMutationResult, error) {
	return c.fileMutation(ctx, OpFileCopy, site, FileTransferRequest{Site: site, Paths: paths, Destination: destination, Overwrite: overwrite})
}

func (c Client) MoveFiles(ctx context.Context, site model.Site, paths []string, destination string, overwrite bool) (FileMutationResult, error) {
	return c.fileMutation(ctx, OpFileMove, site, FileTransferRequest{Site: site, Paths: paths, Destination: destination, Overwrite: overwrite})
}

func (c Client) fileMutation(ctx context.Context, operation Operation, site model.Site, payload any) (FileMutationResult, error) {
	var result FileMutationResult
	err := c.Call(ctx, operation, string(operation)+":"+site.ID+":"+requestID(), payload, &result)
	return result, err
}
