package web

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

const (
	fileMutationRequestBytes = 64 << 10
	fileSearchLimit          = 200
)

// registerFileManagerRoutes keeps this feature's complete HTTP surface beside
// its handlers. Every route still crosses session middleware and each handler
// independently authorizes the submitted site.
func (s *Server) registerFileManagerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /sites/{id}/files", s.requireSession(s.filesPage))
	mux.HandleFunc("POST /sites/{id}/files", s.requireSession(s.saveFile))
	mux.HandleFunc("GET /sites/{id}/files/api/list", s.requireSession(s.listFiles))
	mux.HandleFunc("GET /sites/{id}/files/api/search", s.requireSession(s.searchFiles))
	mux.HandleFunc("GET /sites/{id}/files/api/download", s.requireSession(s.downloadFile))
	mux.HandleFunc("POST /sites/{id}/files/api/upload", s.requireSession(s.uploadFileChunk))
	mux.HandleFunc("POST /sites/{id}/files/api/mkdir", s.requireSession(s.createFileDirectory))
	mux.HandleFunc("POST /sites/{id}/files/api/delete", s.requireSession(s.deleteFiles))
	mux.HandleFunc("POST /sites/{id}/files/api/rename", s.requireSession(s.renameFile))
	mux.HandleFunc("POST /sites/{id}/files/api/archive", s.requireSession(s.archiveFiles))
	mux.HandleFunc("POST /sites/{id}/files/api/extract", s.requireSession(s.extractFile))
	mux.HandleFunc("POST /sites/{id}/files/api/copy", s.requireSession(s.copyFiles))
	mux.HandleFunc("POST /sites/{id}/files/api/move", s.requireSession(s.moveFiles))
}

// File paths are site-relative inputs to typed broker operations. This package
// does not access site files directly; the root broker enforces confinement.
func (s *Server) filesPage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageFiles)
	if !ok {
		return
	}
	if s.broker == nil {
		http.Error(w, "file operations are unavailable", http.StatusServiceUnavailable)
		return
	}
	directory := r.URL.Query().Get("path")
	var list broker.FileListResult
	if err := s.broker.Call(r.Context(), broker.OpFileList, fileKey("list", site.ID), broker.FileRequest{Site: site, Path: directory}, &list); err != nil {
		s.logger.Warn("list site files", "site", site.ID, "path", directory, "error", err)
		http.Error(w, "could not list this directory", http.StatusBadGateway)
		return
	}
	data := pageData{Title: "Files · " + site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CanManageFiles: true, Files: list.Entries, FilePath: directory, DirectoryPath: directory, ParentPath: parentDirectory(directory)}
	if r.URL.Query().Get("saved") == "yes" {
		data.Message = "File saved."
	}
	if edit := r.URL.Query().Get("edit"); edit != "" {
		var result broker.FileReadResult
		if err := s.broker.Call(r.Context(), broker.OpFileRead, fileKey("read", site.ID), broker.FileRequest{Site: site, Path: edit}, &result); err != nil {
			data.Error = "This file cannot be opened in the text editor."
		} else {
			data.EditingFile, data.FilePath, data.FileContent = true, edit, result.Content
		}
	}
	s.render(w, "files.html", data)
}

func (s *Server) saveFile(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageFiles)
	if !ok {
		return
	}
	// The legacy editor is deliberately capped before parsing so an oversized
	// form cannot be buffered by net/http while trying to report a useful error.
	r.Body = http.MaxBytesReader(w, r.Body, (1<<20)+(64<<10))
	if err := r.ParseForm(); err != nil {
		http.Error(w, "file contents are too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	path, content := r.FormValue("path"), r.FormValue("content")
	directory := parentDirectory(path)
	if s.broker == nil || s.broker.Call(r.Context(), broker.OpFileWrite, fileKey("write", site.ID), broker.FileWriteRequest{Site: site, Path: path, Content: content}, nil) != nil {
		data := pageData{Title: "Files · " + site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, FilePath: path, DirectoryPath: directory, ParentPath: parentDirectory(directory), EditingFile: true, FileContent: content, Error: "The file was not saved. Your edits are kept below; check syntax and permissions, then try again."}
		if s.broker != nil {
			var list broker.FileListResult
			if err := s.broker.Call(r.Context(), broker.OpFileList, fileKey("list", site.ID), broker.FileRequest{Site: site, Path: directory}, &list); err == nil {
				data.Files = list.Entries
			}
		}
		s.renderStatus(w, "files.html", http.StatusBadRequest, data)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/files?path="+url.QueryEscape(directory)+"&edit="+url.QueryEscape(path)+"&saved=yes", http.StatusSeeOther)
}

func (s *Server) searchFiles(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageFiles)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" || len(query) > 200 {
		writeFileError(w, http.StatusBadRequest, "Enter a search between 1 and 200 characters.")
		return
	}
	var result broker.FileSearchResult
	err := s.callFile(r, broker.OpFileSearch, "search", site, broker.FileSearchRequest{Site: site, Path: r.URL.Query().Get("path"), Query: query, Limit: fileSearchLimit}, &result)
	if err != nil {
		writeFileBrokerError(w, err, false)
		return
	}
	writeFileJSON(w, http.StatusOK, result)
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageFiles)
	if !ok {
		return
	}
	var result broker.FileListResult
	if err := s.callFile(r, broker.OpFileList, "list", site, broker.FileRequest{Site: site, Path: r.URL.Query().Get("path")}, &result); err != nil {
		writeFileBrokerError(w, err, false)
		return
	}
	writeFileJSON(w, http.StatusOK, result)
}

func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageFiles)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	var offset int64
	var expectedSize int64 = -1
	started := false
	for {
		var part broker.FileDownloadResult
		err := s.callFile(r, broker.OpFileDownload, "download", site, broker.FileDownloadRequest{Site: site, Path: path, Offset: offset, Limit: broker.FileChunkBytes}, &part)
		if err != nil {
			if !started {
				writeFileBrokerError(w, err, false)
			}
			return
		}
		if expectedSize < 0 {
			expectedSize = part.Size
		}
		invalidChunk := part.Offset != offset || part.Next != offset+int64(len(part.Data)) || part.Size != expectedSize || part.Next > part.Size || (!part.EOF && len(part.Data) == 0) || (part.EOF && part.Next != part.Size)
		if invalidChunk {
			if !started {
				writeFileError(w, http.StatusBadGateway, "The download returned an invalid chunk. Try again.")
			}
			return
		}
		if !started {
			name := pathpkg.Base(path)
			w.Header().Set("Content-Type", mime.TypeByExtension(pathpkg.Ext(name)))
			if w.Header().Get("Content-Type") == "" {
				w.Header().Set("Content-Type", "application/octet-stream")
			}
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
			w.Header().Set("Content-Length", strconv.FormatInt(part.Size, 10))
			w.Header().Set("Cache-Control", "no-store")
			started = true
		}
		if _, err := w.Write(part.Data); err != nil {
			return
		}
		offset = part.Next
		if part.EOF {
			return
		}
	}
}

func (s *Server) uploadFileChunk(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedFileMutation(w, r, user)
	if !ok {
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeFileError(w, http.StatusBadRequest, "Invalid upload offset.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, broker.FileChunkBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeFileError(w, http.StatusRequestEntityTooLarge, "Upload chunks cannot exceed 512 KiB.")
		return
	}
	request := broker.FileUploadChunkRequest{Site: site, Path: r.URL.Query().Get("path"), UploadID: r.URL.Query().Get("upload_id"), Offset: offset, Data: data, Final: r.URL.Query().Get("final") == "1", Overwrite: r.URL.Query().Get("overwrite") == "1"}
	var result broker.FileUploadChunkResult
	if err := s.callFile(r, broker.OpFileUploadChunk, "upload", site, request, &result); err != nil {
		writeFileBrokerError(w, err, false)
		return
	}
	writeFileJSON(w, http.StatusOK, result)
}

type filePathsInput struct {
	Paths []string `json:"paths"`
}
type fileMkdirInput struct {
	Path string `json:"path"`
}
type fileRenameInput struct {
	Path    string `json:"path"`
	NewName string `json:"new_name"`
}
type fileArchiveInput struct {
	Paths       []string `json:"paths"`
	Destination string   `json:"destination"`
}
type fileExtractInput struct {
	Path        string `json:"path"`
	Destination string `json:"destination"`
}
type fileTransferInput struct {
	Paths       []string `json:"paths"`
	Destination string   `json:"destination"`
	Overwrite   bool     `json:"overwrite"`
}

func (s *Server) deleteFiles(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedFileMutation(w, r, user)
	if !ok {
		return
	}
	var in filePathsInput
	if !decodeFileJSON(w, r, &in) {
		return
	}
	s.fileMutation(w, r, site, broker.OpFileDelete, "delete", broker.FilePathsRequest{Site: site, Paths: in.Paths})
}
func (s *Server) createFileDirectory(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedFileMutation(w, r, user)
	if !ok {
		return
	}
	var in fileMkdirInput
	if !decodeFileJSON(w, r, &in) {
		return
	}
	s.fileMutation(w, r, site, broker.OpFileMkdir, "mkdir", broker.FileMkdirRequest{Site: site, Path: in.Path})
}
func (s *Server) renameFile(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedFileMutation(w, r, user)
	if !ok {
		return
	}
	var in fileRenameInput
	if !decodeFileJSON(w, r, &in) {
		return
	}
	s.fileMutation(w, r, site, broker.OpFileRename, "rename", broker.FileRenameRequest{Site: site, Path: in.Path, NewName: in.NewName})
}
func (s *Server) archiveFiles(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedFileMutation(w, r, user)
	if !ok {
		return
	}
	var in fileArchiveInput
	if !decodeFileJSON(w, r, &in) {
		return
	}
	s.fileMutation(w, r, site, broker.OpFileArchive, "archive", broker.FileArchiveRequest{Site: site, Paths: in.Paths, Destination: in.Destination})
}
func (s *Server) extractFile(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedFileMutation(w, r, user)
	if !ok {
		return
	}
	var in fileExtractInput
	if !decodeFileJSON(w, r, &in) {
		return
	}
	s.fileMutation(w, r, site, broker.OpFileExtract, "extract", broker.FileExtractRequest{Site: site, Path: in.Path, Destination: in.Destination})
}
func (s *Server) copyFiles(w http.ResponseWriter, r *http.Request, user store.User) {
	s.transferFiles(w, r, user, broker.OpFileCopy, "copy")
}
func (s *Server) moveFiles(w http.ResponseWriter, r *http.Request, user store.User) {
	s.transferFiles(w, r, user, broker.OpFileMove, "move")
}
func (s *Server) transferFiles(w http.ResponseWriter, r *http.Request, user store.User, operation broker.Operation, label string) {
	site, ok := s.authorizedFileMutation(w, r, user)
	if !ok {
		return
	}
	var in fileTransferInput
	if !decodeFileJSON(w, r, &in) {
		return
	}
	s.fileMutation(w, r, site, operation, label, broker.FileTransferRequest{Site: site, Paths: in.Paths, Destination: in.Destination, Overwrite: in.Overwrite})
}

func (s *Server) authorizedFileMutation(w http.ResponseWriter, r *http.Request, user store.User) (model.Site, bool) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageFiles)
	if !ok {
		return model.Site{}, false
	}
	if !s.validCSRF(r) {
		writeFileError(w, http.StatusForbidden, "Invalid request token.")
		return model.Site{}, false
	}
	return site, true
}

func decodeFileJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, fileMutationRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeFileError(w, http.StatusBadRequest, "Invalid file operation.")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeFileError(w, http.StatusBadRequest, "Invalid file operation.")
		return false
	}
	return true
}

func (s *Server) fileMutation(w http.ResponseWriter, r *http.Request, site model.Site, operation broker.Operation, label string, payload any) {
	var result broker.FileMutationResult
	if err := s.callFile(r, operation, label, site, payload, &result); err != nil {
		writeFileBrokerError(w, err, true)
		return
	}
	writeFileJSON(w, http.StatusOK, result)
}

func (s *Server) callFile(r *http.Request, operation broker.Operation, label string, site model.Site, payload, result any) error {
	if s.broker == nil {
		return broker.ErrUnavailable
	}
	return s.broker.Call(r.Context(), operation, fileKey(label, site.ID), payload, result)
}

func fileKey(action, siteID string) string {
	return "file." + action + ":" + siteID + ":" + mustRandomHex(12)
}

func writeFileBrokerError(w http.ResponseWriter, err error, mutation bool) {
	message := "The file operation failed. Try again."
	if mutation || errors.Is(err, broker.ErrOutcomeUnknown) {
		message = "The result could not be confirmed. Refresh the directory before trying again; some items may already have changed."
	}
	writeFileError(w, http.StatusBadGateway, message)
}
func writeFileError(w http.ResponseWriter, status int, message string) {
	writeFileJSON(w, status, map[string]string{"error": message})
}
func writeFileJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func parentDirectory(directory string) string {
	if directory == "" || directory == "." {
		return ""
	}
	parent := pathpkg.Dir(directory)
	if parent == "." {
		return ""
	}
	return parent
}
