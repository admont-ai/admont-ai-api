package mcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/christianfischer/md-wiki-server/internal/draft"
	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/backend"
	"github.com/christianfischer/md-wiki-server/internal/repoactions"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	log "github.com/sirupsen/logrus"
)

func (s *Server) registerTools() {
	s.mcpServer.AddTools(
		// Repos
		tool("list_repos", "List accessible repositories", s.listRepos),
		tool("get_repo", "List all files and folders in a repository",
			s.getRepo,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
		),

		// File read operations
		tool("read_file", "Read file content (returns draft if one exists for the authenticated user)",
			s.readFile,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("File path within the repository")),
		),
		tool("get_file_info", "Get file metadata including git history, size, and draft status",
			s.getFileInfo,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("File path")),
		),
		tool("get_file_history", "Get git commit history for a file",
			s.getFileHistory,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("File path")),
		),
		tool("get_file_diff", "Get diff between a specific commit and HEAD for a file",
			s.getFileDiff,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("File path")),
			mcplib.WithString("commit", mcplib.Required(), mcplib.Description("Commit hash to diff against HEAD")),
		),
		tool("get_file_at_commit", "Get file content at a specific commit",
			s.getFileAtCommit,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("File path")),
			mcplib.WithString("commit", mcplib.Required(), mcplib.Description("Commit hash")),
		),

		// File write operations
		tool("create_file", "Create a new file",
			s.createFile,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("File path to create")),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("File content")),
		),
		tool("update_file", "Update an existing file",
			s.updateFile,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("File path to update")),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("New file content")),
		),
		tool("delete_file", "Delete a file",
			s.deleteFile,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("File path to delete")),
		),
		tool("move_file", "Move a file to a different folder",
			s.moveFile,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("Current file path")),
			mcplib.WithString("destination", mcplib.Required(), mcplib.Description("Destination folder path")),
		),
		tool("rename_file", "Rename a file",
			s.renameFile,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("Current file path")),
			mcplib.WithString("name", mcplib.Required(), mcplib.Description("New filename")),
		),
		tool("upload_file", "Upload a binary file (base64-encoded content)",
			s.uploadFile,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("folder", mcplib.Description("Destination folder path (default: root)")),
			mcplib.WithString("filename", mcplib.Required(), mcplib.Description("Filename to save as")),
			mcplib.WithString("content_base64", mcplib.Required(), mcplib.Description("Base64-encoded file content")),
		),

		// Folder operations
		tool("create_folder", "Create a new folder",
			s.createFolder,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Description("Parent folder path (default: root)")),
			mcplib.WithString("name", mcplib.Required(), mcplib.Description("New folder name")),
		),
		tool("rename_folder", "Rename a folder",
			s.renameFolder,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("Current folder path")),
			mcplib.WithString("name", mcplib.Required(), mcplib.Description("New folder name")),
		),
		tool("delete_folder", "Delete a folder and all its contents",
			s.deleteFolder,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("Folder path to delete")),
		),
		tool("move_folder", "Move a folder to a different location",
			s.moveFolder,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("Current folder path")),
			mcplib.WithString("destination", mcplib.Required(), mcplib.Description("Destination parent folder path")),
		),

		// Draft operations
		tool("save_draft", "Save or update a draft for a file",
			s.saveDraft,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("File path")),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("Draft content")),
		),
		tool("publish_draft", "Publish a draft (three-way merge with HEAD)",
			s.publishDraft,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("File path")),
		),
		tool("delete_draft", "Discard a draft",
			s.deleteDraft,
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository slug")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("File path")),
		),

		// Search
		tool("search", "Search documents across repositories (fulltext, semantic, or hybrid)",
			s.search,
			mcplib.WithString("query", mcplib.Required(), mcplib.Description("Search query")),
			mcplib.WithString("mode", mcplib.Description("Search mode: fulltext, semantic, or hybrid (default: hybrid)")),
			mcplib.WithNumber("top_k", mcplib.Description("Maximum number of results (default: 10, max: 100)")),
			mcplib.WithNumber("threshold", mcplib.Description("Minimum score threshold (default: 0)")),
		),
		tool("search_status", "Get search index status for all repositories", s.searchStatus),
	)
}

// tool is a helper to build a ServerTool from a name, description, handler, and options.
func tool(name, desc string, handler mcpserver.ToolHandlerFunc, opts ...mcplib.ToolOption) mcpserver.ServerTool {
	allOpts := append([]mcplib.ToolOption{mcplib.WithDescription(desc)}, opts...)
	return mcpserver.ServerTool{
		Tool:    mcplib.NewTool(name, allOpts...),
		Handler: handler,
	}
}

// --- Repo Tools ---

func (s *Server) listRepos(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	identity := getUserIdentity(ctx)
	var repos []map[string]any
	for slug, rc := range s.repoConfigs {
		if !rc.PublicAccess && identity == "" {
			continue
		}
		ready := true
		if v, ok := s.repoReady.Load(slug); ok {
			ready, _ = v.(bool)
		}
		name := rc.Name
		if name == "" {
			name = slug
		}
		repos = append(repos, map[string]any{
			"slug":      slug,
			"name":      name,
			"ready":     ready,
			"read_only": rc.ReadOnly,
		})
	}
	if repos == nil {
		repos = []map[string]any{}
	}
	return jsonResult(repos), nil
}

func (s *Server) getRepo(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	identity := getUserIdentity(ctx)
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	backend, ok := s.backends[repo]
	if !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	root := s.docPaths[repo]
	entries, err := backend.ListEntries(root)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to list files: %v", err)), nil
	}

	isAdmin := identity != "" && s.isSystemAdmin != nil && s.isSystemAdmin(identity)
	var result []map[string]any
	for _, e := range entries {
		entryPath := filepath.Join(e.Path, e.Name)
		if !isAdmin && isDotPath(entryPath) {
			continue
		}
		permPath := entryPath
		if e.IsDir {
			permPath += "/"
		}
		resolver := s.permResolvers[repo]
		if !isAdmin && resolver != nil && !resolver.Check(identity, permPath, permissions.Viewer) {
			if !e.IsDir || !resolver.HasAccessibleDescendant(identity, entryPath, permissions.Viewer) {
				continue
			}
		}
		entry := map[string]any{
			"name":   e.Name,
			"path":   entryPath,
			"is_dir": e.IsDir,
		}
		if !e.IsDir {
			entry["size"] = e.Size
		}
		result = append(result, entry)
	}
	if result == nil {
		result = []map[string]any{}
	}
	return jsonResult(result), nil
}

// --- File Read Tools ---

func (s *Server) readFile(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	identity := getUserIdentity(ctx)
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if err := s.checkPermission(repo, identity, path, permissions.Viewer); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	backend, ok := s.backends[repo]
	if !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	subfolder, filename := splitPath(path)

	// Return draft if one exists
	if identity != "" {
		if dm, ok := s.draftManagers[repo]; ok && dm.HasDraft(subfolder, filename, identity) {
			draftContent, _, err := dm.GetDraft(subfolder, filename, identity)
			if err == nil {
				return mcplib.NewToolResultText(string(draftContent)), nil
			}
		}
	}

	content, err := backend.GetFile(subfolder, filename)
	if err != nil {
		return mcplib.NewToolResultError("file not found"), nil
	}
	return mcplib.NewToolResultText(string(content)), nil
}

func (s *Server) getFileInfo(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	identity := getUserIdentity(ctx)
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if err := s.checkPermission(repo, identity, path, permissions.Viewer); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	backend, ok := s.backends[repo]
	if !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}

	cleanPath := strings.TrimSuffix(path, "/")
	info, err := backend.Stat(cleanPath)
	if err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}

	resp := map[string]any{
		"name":   filepath.Base(cleanPath),
		"path":   path,
		"is_dir": info.IsDir,
	}
	if !info.IsDir {
		resp["size"] = info.Size
	}

	subfolder, filename := splitPath(cleanPath)
	history, err := backend.GetFileHistory(subfolder, filename)
	if err == nil && len(history) > 0 {
		latest := history[0]
		resp["last_commit_hash"] = latest.CommitHash
		resp["last_author"] = latest.Author
		resp["last_author_email"] = latest.AuthorEmail
		resp["last_modified"] = latest.Date.Format("2006-01-02T15:04:05Z07:00")
		resp["last_message"] = latest.Message
		oldest := history[len(history)-1]
		resp["created_at"] = oldest.Date.Format("2006-01-02T15:04:05Z07:00")
	}

	if !info.IsDir && identity != "" {
		if dm, ok := s.draftManagers[repo]; ok {
			if meta, err := dm.GetDraftMeta(subfolder, filename, identity); err == nil {
				resp["is_draft"] = true
				resp["draft_created_at"] = meta.CreatedAt.Format("2006-01-02T15:04:05Z07:00")
				resp["draft_updated_at"] = meta.UpdatedAt.Format("2006-01-02T15:04:05Z07:00")
			}
		}
	}

	// Surface whether ANOTHER user has a pending draft — owner identity and
	// timestamp only, never the draft's content.
	if !info.IsDir {
		if dm, ok := s.draftManagers[repo]; ok {
			if other, err := dm.OtherUsersDraft(subfolder, filename, identity); err == nil && other != nil {
				resp["pending_draft_owner_name"] = other.UserName
				resp["pending_draft_owner_email"] = other.UserEmail
				resp["pending_draft_updated_at"] = other.UpdatedAt.Format("2006-01-02T15:04:05Z07:00")
				resp["locked_by_pending_draft"] = !(identity != "" && s.isSystemAdmin != nil && s.isSystemAdmin(identity))
			}
		}
	}

	return jsonResult(resp), nil
}

func (s *Server) getFileHistory(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	identity := getUserIdentity(ctx)
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if err := s.checkPermission(repo, identity, path, permissions.Viewer); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	backend, ok := s.backends[repo]
	if !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if !backend.SupportsHistory() {
		return mcplib.NewToolResultError("history not supported for this repository backend"), nil
	}
	subfolder, filename := splitPath(path)
	history, err := backend.GetFileHistory(subfolder, filename)
	if err != nil {
		return mcplib.NewToolResultError("file not found"), nil
	}
	var entries []map[string]any
	for _, h := range history {
		entries = append(entries, map[string]any{
			"commit_hash":  h.CommitHash,
			"author":       h.Author,
			"author_email": h.AuthorEmail,
			"date":         h.Date.Format("2006-01-02T15:04:05Z07:00"),
			"message":      h.Message,
		})
	}
	return jsonResult(map[string]any{"entries": entries}), nil
}

func (s *Server) getFileDiff(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	commit := req.GetString("commit", "")
	identity := getUserIdentity(ctx)
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if err := s.checkPermission(repo, identity, path, permissions.Viewer); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	backend, ok := s.backends[repo]
	if !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if !backend.SupportsHistory() {
		return mcplib.NewToolResultError("history not supported for this repository backend"), nil
	}
	subfolder, filename := splitPath(path)
	diff, err := backend.GetFileDiffWithCommit(commit, subfolder, filename)
	if err != nil {
		return mcplib.NewToolResultError("file or commit not found"), nil
	}
	return jsonResult(map[string]any{"diff": diff}), nil
}

func (s *Server) getFileAtCommit(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	commit := req.GetString("commit", "")
	identity := getUserIdentity(ctx)
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if err := s.checkPermission(repo, identity, path, permissions.Viewer); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	backend, ok := s.backends[repo]
	if !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if !backend.SupportsHistory() {
		return mcplib.NewToolResultError("history not supported for this repository backend"), nil
	}
	subfolder, filename := splitPath(path)
	content, err := backend.GetFileAtCommit(commit, subfolder, filename)
	if err != nil {
		return mcplib.NewToolResultError("file or commit not found"), nil
	}
	return jsonResult(map[string]any{"commit_hash": commit, "content": content}), nil
}

// --- File Write Tools ---

func (s *Server) createFile(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	content := req.GetString("content", "")
	identity := getUserIdentity(ctx)
	email := getUserEmail(ctx)
	name := getUserName(ctx)
	if identity == "" {
		return mcplib.NewToolResultError("authentication required"), nil
	}
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if _, ok := s.backends[repo]; !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	subfolder, filename := splitPath(path)
	parentFolder := subfolder
	if parentFolder == "" {
		parentFolder = "."
	}
	if err := s.checkPermission(repo, identity, parentFolder+"/", permissions.Contributor); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	if err := repoactions.CreateFile(s.actionDeps(repo, identity), subfolder, filename, path, []byte(content), "create "+path, name, email); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to create file: %v", err)), nil
	}
	return jsonResult(map[string]any{"name": filename, "path": path}), nil
}

func (s *Server) updateFile(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	content := req.GetString("content", "")
	identity := getUserIdentity(ctx)
	email := getUserEmail(ctx)
	name := getUserName(ctx)
	if identity == "" {
		return mcplib.NewToolResultError("authentication required"), nil
	}
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if _, ok := s.backends[repo]; !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	subfolder, filename := splitPath(path)
	if err := s.checkPermission(repo, identity, path, permissions.Contributor); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	if msg := s.draftLockMessage(repo, subfolder, filename, identity); msg != "" {
		return mcplib.NewToolResultError(msg), nil
	}
	if err := repoactions.UpdateFile(s.actionDeps(repo, identity), subfolder, filename, path, []byte(content), "update "+path, name, email); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to update file: %v", err)), nil
	}
	return jsonResult(map[string]any{"name": filename, "path": path}), nil
}

func (s *Server) deleteFile(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	identity := getUserIdentity(ctx)
	email := getUserEmail(ctx)
	name := getUserName(ctx)
	if identity == "" {
		return mcplib.NewToolResultError("authentication required"), nil
	}
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if _, ok := s.backends[repo]; !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	subfolder, filename := splitPath(path)
	if err := s.checkPermission(repo, identity, path, permissions.ContentManager); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	if msg := s.draftLockMessage(repo, subfolder, filename, identity); msg != "" {
		return mcplib.NewToolResultError(msg), nil
	}
	if err := repoactions.DeleteFile(s.actionDeps(repo, identity), subfolder, filename, path, "delete "+path, name, email); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to delete file: %v", err)), nil
	}
	return mcplib.NewToolResultText("deleted"), nil
}

func (s *Server) moveFile(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	dest := strings.TrimPrefix(req.GetString("destination", ""), "/")
	identity := getUserIdentity(ctx)
	email := getUserEmail(ctx)
	name := getUserName(ctx)
	if identity == "" {
		return mcplib.NewToolResultError("authentication required"), nil
	}
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if _, ok := s.backends[repo]; !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	oldSubfolder, filename := splitPath(path)
	newFilePath := filepath.Join(dest, filename)
	if err := s.checkPermission(repo, identity, path, permissions.ContentManager); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	destFolder := dest
	if destFolder == "" {
		destFolder = "."
	}
	if err := s.checkPermission(repo, identity, destFolder+"/", permissions.Contributor); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	if msg := s.draftLockMessage(repo, oldSubfolder, filename, identity); msg != "" {
		return mcplib.NewToolResultError(msg), nil
	}
	if err := repoactions.MoveFile(s.actionDeps(repo, identity), oldSubfolder, filename, dest, filename, path, newFilePath, "move "+path+" to "+newFilePath, name, email); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to move file: %v", err)), nil
	}
	return jsonResult(map[string]any{"name": filename, "path": newFilePath}), nil
}

func (s *Server) renameFile(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	newName := req.GetString("name", "")
	identity := getUserIdentity(ctx)
	email := getUserEmail(ctx)
	name := getUserName(ctx)
	if identity == "" {
		return mcplib.NewToolResultError("authentication required"), nil
	}
	if newName == "" {
		return mcplib.NewToolResultError("name is required"), nil
	}
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if _, ok := s.backends[repo]; !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	subfolder, oldName := splitPath(path)
	newFilePath := filepath.Join(subfolder, newName)
	if err := s.checkPermission(repo, identity, path, permissions.Contributor); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	if msg := s.draftLockMessage(repo, subfolder, oldName, identity); msg != "" {
		return mcplib.NewToolResultError(msg), nil
	}
	if err := repoactions.MoveFile(s.actionDeps(repo, identity), subfolder, oldName, subfolder, newName, path, newFilePath, "rename "+path+" to "+newFilePath, name, email); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to rename file: %v", err)), nil
	}
	return jsonResult(map[string]any{"name": newName, "path": newFilePath}), nil
}

func (s *Server) uploadFile(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	folder := req.GetString("folder", "")
	filename := req.GetString("filename", "")
	contentB64 := req.GetString("content_base64", "")
	identity := getUserIdentity(ctx)
	email := getUserEmail(ctx)
	name := getUserName(ctx)
	if identity == "" {
		return mcplib.NewToolResultError("authentication required"), nil
	}
	if filename == "" || contentB64 == "" {
		return mcplib.NewToolResultError("filename and content_base64 are required"), nil
	}
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	backend, ok := s.backends[repo]
	if !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	parentFolder := folder
	if parentFolder == "" {
		parentFolder = "."
	}
	if err := s.checkPermission(repo, identity, parentFolder+"/", permissions.Contributor); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	data, err := base64.StdEncoding.DecodeString(contentB64)
	if err != nil {
		return mcplib.NewToolResultError("invalid base64 content"), nil
	}
	if err := backend.AddFile(folder, filename, data); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to upload file: %v", err)), nil
	}
	docPath := filepath.Join(folder, filename)
	backend.SaveChangesAsync("upload "+docPath, name, email)
	return jsonResult(map[string]any{"name": filename, "path": docPath}), nil
}

// --- Folder Tools ---

func (s *Server) createFolder(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	folderName := req.GetString("name", "")
	identity := getUserIdentity(ctx)
	email := getUserEmail(ctx)
	name := getUserName(ctx)
	if identity == "" {
		return mcplib.NewToolResultError("authentication required"), nil
	}
	if folderName == "" {
		return mcplib.NewToolResultError("name is required"), nil
	}
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if _, ok := s.backends[repo]; !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	subfolder := filepath.Join(path, folderName)
	parentFolder := path
	if parentFolder == "" {
		parentFolder = "."
	}
	if err := s.checkPermission(repo, identity, parentFolder+"/", permissions.Contributor); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	if err := repoactions.CreateFolder(s.actionDeps(repo, identity), subfolder, "create folder "+subfolder, name, email); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to create folder: %v", err)), nil
	}
	return jsonResult(map[string]any{"name": folderName, "path": subfolder}), nil
}

func (s *Server) renameFolder(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	newName := req.GetString("name", "")
	identity := getUserIdentity(ctx)
	email := getUserEmail(ctx)
	name := getUserName(ctx)
	if identity == "" {
		return mcplib.NewToolResultError("authentication required"), nil
	}
	if newName == "" {
		return mcplib.NewToolResultError("name is required"), nil
	}
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if _, ok := s.backends[repo]; !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	parentDir := filepath.Dir(path)
	if parentDir == "." {
		parentDir = ""
	}
	newRelPath := filepath.Join(parentDir, newName)
	if err := s.checkPermission(repo, identity, path+"/", permissions.Contributor); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	if msg := s.draftLockMessageForFolder(repo, path, identity); msg != "" {
		return mcplib.NewToolResultError(msg), nil
	}
	if err := repoactions.RenameFolder(s.actionDeps(repo, identity), path, newRelPath, "rename folder "+path+" to "+newRelPath, name, email); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to rename folder: %v", err)), nil
	}
	return jsonResult(map[string]any{"name": newName, "path": newRelPath}), nil
}

func (s *Server) deleteFolder(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	identity := getUserIdentity(ctx)
	email := getUserEmail(ctx)
	name := getUserName(ctx)
	if identity == "" {
		return mcplib.NewToolResultError("authentication required"), nil
	}
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if _, ok := s.backends[repo]; !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if err := s.checkPermission(repo, identity, path+"/", permissions.ContentManager); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	if msg := s.draftLockMessageForFolder(repo, path, identity); msg != "" {
		return mcplib.NewToolResultError(msg), nil
	}
	if err := repoactions.DeleteFolder(s.actionDeps(repo, identity), path, "delete folder "+path, name, email); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to delete folder: %v", err)), nil
	}
	return mcplib.NewToolResultText("deleted"), nil
}

func (s *Server) moveFolder(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	dest := strings.TrimPrefix(req.GetString("destination", ""), "/")
	identity := getUserIdentity(ctx)
	email := getUserEmail(ctx)
	name := getUserName(ctx)
	if identity == "" {
		return mcplib.NewToolResultError("authentication required"), nil
	}
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	if _, ok := s.backends[repo]; !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	folderName := filepath.Base(path)
	newRelPath := filepath.Join(dest, folderName)
	if err := s.checkPermission(repo, identity, path+"/", permissions.ContentManager); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	destParent := dest
	if destParent == "" {
		destParent = "."
	}
	if err := s.checkPermission(repo, identity, destParent+"/", permissions.Contributor); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	if msg := s.draftLockMessageForFolder(repo, path, identity); msg != "" {
		return mcplib.NewToolResultError(msg), nil
	}
	if err := repoactions.RenameFolder(s.actionDeps(repo, identity), path, newRelPath, "move folder "+path+" to "+newRelPath, name, email); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to move folder: %v", err)), nil
	}
	return jsonResult(map[string]any{"name": folderName, "path": newRelPath}), nil
}

// --- Draft Tools ---

func (s *Server) saveDraft(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	content := req.GetString("content", "")
	identity := getUserIdentity(ctx)
	name := getUserName(ctx)
	if identity == "" {
		return mcplib.NewToolResultError("authentication required"), nil
	}
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	backend, ok := s.backends[repo]
	if !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	dm, ok := s.draftManagers[repo]
	if !ok {
		return mcplib.NewToolResultError("drafts not available for this repository"), nil
	}
	subfolder, filename := splitPath(path)
	if err := s.checkPermission(repo, identity, path, permissions.Contributor); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	if msg := s.draftLockMessage(repo, subfolder, filename, identity); msg != "" {
		return mcplib.NewToolResultError(msg), nil
	}
	baseCommitHash := ""
	if hash, err := backend.GetFileCommitHash(subfolder, filename); err == nil {
		baseCommitHash = hash
	}
	if err := dm.SaveDraft(subfolder, filename, identity, name, baseCommitHash, []byte(content)); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to save draft: %v", err)), nil
	}
	resp := map[string]any{"name": filename, "path": path, "is_draft": true}
	if meta, err := dm.GetDraftMeta(subfolder, filename, identity); err == nil {
		resp["draft_created_at"] = meta.CreatedAt.Format("2006-01-02T15:04:05Z07:00")
		resp["draft_updated_at"] = meta.UpdatedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	return jsonResult(resp), nil
}

func (s *Server) publishDraft(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	identity := getUserIdentity(ctx)
	email := getUserEmail(ctx)
	name := getUserName(ctx)
	if identity == "" {
		return mcplib.NewToolResultError("authentication required"), nil
	}
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	backend, ok := s.backends[repo]
	if !ok {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	dm, ok := s.draftManagers[repo]
	if !ok {
		return mcplib.NewToolResultError("drafts not available for this repository"), nil
	}
	subfolder, filename := splitPath(path)
	if err := s.checkPermission(repo, identity, path, permissions.Contributor); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	draftContent, meta, err := dm.GetDraft(subfolder, filename, identity)
	if err != nil {
		return mcplib.NewToolResultError("no draft found"), nil
	}
	baseContent := ""
	if meta != nil && meta.BaseCommitHash != "" {
		if c, err := backend.GetFileAtCommit(meta.BaseCommitHash, subfolder, filename); err == nil {
			baseContent = c
		}
	}
	currentBytes, err := backend.GetFile(subfolder, filename)
	if err != nil {
		currentBytes = []byte{}
	}
	merged, hasConflicts, conflictText := draft.ThreeWayMerge(baseContent, string(currentBytes), string(draftContent))
	if hasConflicts {
		return jsonResult(map[string]any{"success": false, "conflicts": conflictText, "path": path}), nil
	}
	if err := backend.AddFile(subfolder, filename, []byte(merged)); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to write file: %v", err)), nil
	}
	backend.SaveChangesAsync("publish draft "+path, name, email)
	if s.shouldIndex(repo) {
		s.idx.IndexFile(repo, path)
	}
	if err := dm.DeleteDraft(subfolder, filename, identity); err != nil {
		log.WithError(err).Warn("MCP: failed to clean up draft after publish")
	}
	return jsonResult(map[string]any{"success": true, "merged": merged, "path": path}), nil
}

func (s *Server) deleteDraft(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	repo := req.GetString("repo", "")
	path := req.GetString("path", "")
	identity := getUserIdentity(ctx)
	if identity == "" {
		return mcplib.NewToolResultError("authentication required"), nil
	}
	if !s.canAccessRepo(repo, identity) {
		return mcplib.NewToolResultError("repository not found"), nil
	}
	dm, ok := s.draftManagers[repo]
	if !ok {
		return mcplib.NewToolResultError("drafts not available for this repository"), nil
	}
	subfolder, filename := splitPath(path)
	if err := s.checkPermission(repo, identity, path, permissions.Contributor); err != nil {
		return mcplib.NewToolResultError("not found"), nil
	}
	if !dm.HasDraft(subfolder, filename, identity) {
		return mcplib.NewToolResultError("no draft found"), nil
	}
	if err := dm.DeleteDraft(subfolder, filename, identity); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to discard draft: %v", err)), nil
	}
	return mcplib.NewToolResultText("draft discarded"), nil
}

// --- Search Tools ---

func (s *Server) search(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if s.searchBackend == nil || s.searchBackend.Get() == nil {
		return mcplib.NewToolResultError("search is not enabled"), nil
	}
	query := req.GetString("query", "")
	if query == "" {
		return mcplib.NewToolResultError("query is required"), nil
	}
	mode := req.GetString("mode", "hybrid")
	if mode != "fulltext" && mode != "semantic" && mode != "hybrid" {
		return mcplib.NewToolResultError("mode must be fulltext, semantic, or hybrid"), nil
	}
	topK := req.GetInt("top_k", 10)
	if topK <= 0 {
		topK = 10
	}
	if topK > 100 {
		topK = 100
	}
	threshold := req.GetFloat("threshold", 0)
	identity := getUserIdentity(ctx)

	// Search across all accessible repos that have indexing enabled
	var allowedRepos []string
	for slug, rc := range s.repoConfigs {
		if rc.SearchProviderID == nil {
			continue
		}
		if !s.canAccessRepo(slug, identity) {
			continue
		}
		allowedRepos = append(allowedRepos, slug)
	}
	if len(allowedRepos) == 0 {
		return jsonResult(map[string]any{"results": []any{}}), nil
	}

	b := s.searchBackend.Get()
	// Over-fetch so per-file permission filtering can still return topK results.
	fetchK := topK * 4
	if fetchK > 400 {
		fetchK = 400
	}
	var results []backend.SearchResult
	var err error
	switch mode {
	case "fulltext":
		results, err = b.FulltextSearch(ctx, allowedRepos, query, "", fetchK, threshold)
	case "semantic":
		results, err = b.SemanticSearch(ctx, allowedRepos, query, "", fetchK, threshold)
	case "hybrid":
		results, err = b.HybridSearch(ctx, allowedRepos, query, "", fetchK, threshold)
	}
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
	}
	// Enforce per-document access: drop chunks from files the user cannot read.
	results = s.filterSearchResults(results, identity, topK)
	return jsonResult(map[string]any{"results": results}), nil
}

func (s *Server) searchStatus(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if s.repoState == nil {
		return mcplib.NewToolResultError("search is not enabled"), nil
	}
	states, err := s.repoState.GetAllSearchRepoStates(ctx)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to get status: %v", err)), nil
	}
	// Only expose status for repos the caller can access.
	identity := getUserIdentity(ctx)
	filtered := states[:0]
	for _, st := range states {
		if s.canAccessRepo(st.RepoSlug, identity) {
			filtered = append(filtered, st)
		}
	}
	return jsonResult(map[string]any{"repos": filtered}), nil
}
