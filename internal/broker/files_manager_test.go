package broker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type fakeFileManager struct{}

func (fakeFileManager) ListFiles(context.Context, model.Site, string) ([]FileEntry, error) {
	return nil, nil
}
func (fakeFileManager) ReadFile(context.Context, model.Site, string) (string, error) { return "", nil }
func (fakeFileManager) WriteFile(context.Context, model.Site, string, string) error  { return nil }
func (fakeFileManager) SearchFiles(context.Context, model.Site, string, string, int) (FileSearchResult, error) {
	return FileSearchResult{}, nil
}
func (fakeFileManager) DownloadFile(context.Context, model.Site, string, int64, int) (FileDownloadResult, error) {
	return FileDownloadResult{}, nil
}
func (fakeFileManager) UploadFileChunk(context.Context, model.Site, string, string, int64, []byte, bool, bool) (FileUploadChunkResult, error) {
	return FileUploadChunkResult{}, nil
}
func (fakeFileManager) DeleteFiles(context.Context, model.Site, []string) (int, error) { return 1, nil }
func (fakeFileManager) RenameFile(context.Context, model.Site, string, string) error   { return nil }
func (fakeFileManager) ArchiveFiles(context.Context, model.Site, []string, string) error {
	return nil
}
func (fakeFileManager) ExtractFile(context.Context, model.Site, string, string) (int, error) {
	return 1, nil
}
func (fakeFileManager) CopyFiles(context.Context, model.Site, []string, string, bool) (int, error) {
	return 1, nil
}
func (fakeFileManager) MoveFiles(context.Context, model.Site, []string, string, bool) (int, error) {
	return 1, nil
}
func (fakeFileManager) CreateDirectory(context.Context, model.Site, string) error { return nil }

func activeBrokerFileSite() model.Site {
	return model.Site{ID: "files-example", Domain: "files.example.com", Kind: model.Static, Status: "active"}
}

func fileManagerRequest(t *testing.T, operation Operation, payload any) Request {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return Request{Version: ProtocolVersion, ID: "request", IdempotencyKey: "test", Operation: operation, Payload: raw}
}

func TestFileManagerBrokerRejectsTraversalAndOversizedChunks(t *testing.T) {
	server := &Server{Files: fakeFileManager{}}
	site := activeBrokerFileSite()
	tests := []Request{
		fileManagerRequest(t, OpFileDownload, FileDownloadRequest{Site: site, Path: "../secret", Limit: 1}),
		fileManagerRequest(t, OpFileUploadChunk, FileUploadChunkRequest{Site: site, Path: "file", UploadID: "upload_0123456789", Data: make([]byte, FileChunkBytes+1)}),
		fileManagerRequest(t, OpFileDelete, FilePathsRequest{Site: site, Paths: []string{"dir", "dir/file"}}),
		fileManagerRequest(t, OpFileRename, FileRenameRequest{Site: site, Path: "file", NewName: "../other"}),
		fileManagerRequest(t, OpFileArchive, FileArchiveRequest{Site: site, Paths: []string{"file"}, Destination: "archive.tar"}),
	}
	for _, request := range tests {
		response, handled := server.dispatchFileManager(request)
		if !handled || response.OK {
			t.Fatalf("unsafe %s request was accepted: %+v", request.Operation, response)
		}
	}
}

func TestFileManagerBrokerAcceptsRootAndRequiresActiveAuthorizedSite(t *testing.T) {
	server := &Server{Files: fakeFileManager{}}
	site := activeBrokerFileSite()
	response, handled := server.dispatchFileManager(fileManagerRequest(t, OpFileSearch, FileSearchRequest{Site: site, Path: "", Query: "index", Limit: 20}))
	if !handled || !response.OK {
		t.Fatalf("root search rejected: %+v", response)
	}
	site.Status = "disabled"
	response, _ = server.dispatchFileManager(fileManagerRequest(t, OpFileMkdir, FileMkdirRequest{Site: site, Path: "assets"}))
	if response.OK {
		t.Fatal("disabled site mutation was accepted")
	}
}

func TestFileManagerBrokerRejectsTrailingJSON(t *testing.T) {
	request := fileManagerRequest(t, OpFileSearch, FileSearchRequest{Site: activeBrokerFileSite(), Path: "", Query: "x", Limit: 1})
	request.Payload = append(request.Payload, []byte(` {}`)...)
	response, _ := (&Server{Files: fakeFileManager{}}).dispatchFileManager(request)
	if response.OK {
		t.Fatal("trailing JSON value was accepted")
	}
}
