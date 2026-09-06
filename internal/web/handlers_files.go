package web

import (
	"net/http"
	"net/url"
	pathpkg "path"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

// File paths are site-relative inputs to typed broker operations. This package
// does not access site files directly; the broker enforces confinement and writes.

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
	if err := s.broker.Call(r.Context(), broker.OpFileList, "file.list:"+site.ID+":"+mustRandomHex(8), broker.FileRequest{Site: site, Path: directory}, &list); err != nil {
		s.logger.Warn("list site files", "site", site.ID, "path", directory, "error", err)
		http.Error(w, "could not list this directory", http.StatusBadGateway)
		return
	}
	data := pageData{Title: "Files · " + site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CanManageUsers: rbac.Allows(user.Role, rbac.ManageUsers), CanManageFiles: true, Files: list.Entries, FilePath: directory, DirectoryPath: directory, ParentPath: parentDirectory(directory)}
	if r.URL.Query().Get("saved") == "yes" {
		data.Message = "File saved."
	}
	if edit := r.URL.Query().Get("edit"); edit != "" {
		var result broker.FileReadResult
		if err := s.broker.Call(r.Context(), broker.OpFileRead, "file.read:"+site.ID+":"+mustRandomHex(8), broker.FileRequest{Site: site, Path: edit}, &result); err != nil {
			data.Error = "This file cannot be opened in the text editor."
		} else {
			data.EditingFile = true
			data.FilePath = edit
			data.FileContent = result.Content
		}
	}
	s.render(w, "files.html", data)
}

func (s *Server) saveFile(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageFiles)
	if !ok {
		return
	}
	path := r.FormValue("path")
	content := r.FormValue("content")
	directory := parentDirectory(path)
	if s.broker == nil || s.broker.Call(r.Context(), broker.OpFileWrite, "file.write:"+site.ID+":"+mustRandomHex(12), broker.FileWriteRequest{Site: site, Path: path, Content: content}, nil) != nil {
		// Never reload the file after a rejected write: that would replace the
		// user's unsaved work with the old content. A failed directory refresh
		// also must not prevent them from recovering or correcting their text.
		data := pageData{
			Title: "Files · " + site.Domain, User: &user, CSRF: s.ensureCSRF(w, r),
			Site: &site, FilePath: path, DirectoryPath: directory,
			ParentPath: parentDirectory(directory), EditingFile: true, FileContent: content,
			Error: "The file was not saved. Your edits are kept below; check syntax and permissions, then try again.",
		}
		if s.broker != nil {
			var list broker.FileListResult
			if err := s.broker.Call(r.Context(), broker.OpFileList, "file.list:"+site.ID+":"+mustRandomHex(8), broker.FileRequest{Site: site, Path: directory}, &list); err != nil {
				s.logger.Warn("refresh directory after rejected file save", "site", site.ID, "path", directory, "error", err)
			} else {
				data.Files = list.Entries
			}
		}
		s.renderStatus(w, "files.html", http.StatusBadRequest, data)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/files?path="+url.QueryEscape(directory)+"&edit="+url.QueryEscape(path)+"&saved=yes", http.StatusSeeOther)
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
