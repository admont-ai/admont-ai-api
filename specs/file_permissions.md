# File & Folder Permission System — Specification

## 1. Overview

This feature adds file and folder-level permissions to the md-wiki-server, stored inside each Git repository as a `.wiki-permissions.yaml` file. It replaces the current repo-level role system (admins/editors/readers) with a more granular, path-based access control model.

Every file and folder has an **owner** (the user who created it) with full permissions. Additional users can be granted specific permission levels per path. A default permission level applies to all other authenticated users. Folder permissions **inherit** to child files and subfolders unless explicitly overridden.

## 2. Permission Levels

Permissions follow a **hierarchical model** where higher levels include all lower levels:

| Level    | Value | Capabilities                                      |
|----------|-------|---------------------------------------------------|
| `none`   | 0     | No access                                         |
| `read`   | 1     | View file/folder contents                         |
| `update` | 2     | Read + edit content, create files in folder       |
| `delete` | 3     | Update + delete the file/folder                   |

`delete` implies `update`, which implies `read`.

## 3. Permission File Format

A single file named `.wiki-permissions.yaml` is stored at the root of each Git repository.

```yaml
version: 1

# Default permission level for authenticated users with no specific path entry.
defaults: read    # "none" | "read" | "update" | "delete"

# Per-path permission entries.
# File paths are relative to the repo root.
# Folder paths end with a trailing slash.
paths:
  "docs/":
    owner: alice@example.com
    default: update
    users:
      bob@example.com: delete
      carol@example.com: read

  "private/":
    owner: alice@example.com
    default: none
    users:
      bob@example.com: read

  "private/secret.md":
    owner: alice@example.com
    default: none
    # No users listed — only the owner has access.
```

### 3.1 Field Descriptions

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `version` | int | yes | Schema version, currently `1` |
| `defaults` | string | yes | Global default level for paths with no entry |
| `paths` | map | no | Path-based permission entries |
| `paths.<path>.owner` | string | yes | Email of the file/folder creator |
| `paths.<path>.default` | string | yes | Default level for this path |
| `paths.<path>.users` | map | no | Per-user permission overrides (email → level) |

### 3.2 Path Conventions

- File paths: `"docs/guide.md"` (no leading slash)
- Folder paths: `"docs/"` (trailing slash)
- Root folder: `"/"`

## 4. Permission Resolution Algorithm

For a given user and file path, the effective permission is resolved as follows:

1. **System admin bypass**: If the user is `super_admin` or has the `admin` system role → **allow all**.
2. **Exact file entry**: If `.wiki-permissions.yaml` has an entry matching the exact file path:
   - User is the `owner` → **allow all**
   - User is listed in `users` → use that level
   - Otherwise → use the entry's `default`
3. **Folder inheritance**: Walk parent folders from nearest to root. For the first ancestor folder entry found (e.g., `"docs/"` for file `"docs/guide.md"`):
   - User is the `owner` → **allow all**
   - User is listed in `users` → use that level
   - Otherwise → use the entry's `default`
4. **Global default**: If no path entry matched → use the top-level `defaults` value.
5. **No permissions file**: If `.wiki-permissions.yaml` does not exist → all authenticated users have full access (backward compatibility).

### 4.1 Operation-to-Permission Mapping

| Operation | Required Level | Target Path |
|-----------|---------------|-------------|
| Read file | `read` | File path |
| List folder contents | `read` | Folder path |
| Create file in folder | `update` | Parent folder |
| Create subfolder | `update` | Parent folder |
| Update file | `update` | File path |
| Rename file | `update` | File path |
| Upload file | `update` | Parent folder |
| Delete file | `delete` | File path |
| Delete folder | `delete` | Folder path |
| Move file | `delete` on source + `update` on destination folder | Both paths |
| Move folder | `delete` on source folder + `update` on destination parent | Both paths |
| Rename folder | `update` | Folder path |

## 5. Interaction with Existing Permission Model

### 5.1 Repo-Level Changes

The current `RepoPermissions` struct is simplified. The `admins`, `editors`, and `readers` lists are **removed**. Only the `public` flag remains:

```yaml
# Before
permissions:
  public: read
  admins: [alice@example.com]
  editors: [bob@example.com]
  readers: [carol@example.com]

# After
permissions:
  public: read
```

The `public` flag controls unauthenticated access:
- `"read"`: Unauthenticated users can read files (subject to path-level `default` restrictions)
- `"none"`: Authentication required for all access

### 5.2 System Roles (Unchanged)

- `super_admin` (from `config.yaml`) — bypasses all file permissions
- `admin` role (from `users.yaml`) — bypasses all file permissions
- `repo_admin` role (from `users.yaml`) — can manage repositories

### 5.3 Public Repos with Path Restrictions

For repos with `public: read`, unauthenticated users can read files. However, if a path entry has `default: none`, unauthenticated users are **blocked** from that path. This allows public repos with private sections.

## 6. Automatic Permission Management

### 6.1 File/Folder Creation

When a file or folder is created, the service automatically adds an owner entry to `.wiki-permissions.yaml`:

```yaml
paths:
  "docs/new-guide.md":
    owner: creator@example.com
    default: read   # inherits from top-level defaults
```

The permissions file change is included in the same Git commit as the file creation.

### 6.2 File/Folder Deletion

When a file or folder is deleted, its entry is removed from `.wiki-permissions.yaml`. For folder deletion, all child entries are also removed.

### 6.3 Move/Rename

When a file or folder is moved or renamed, all matching path entries are updated to reflect the new path. For folder renames, all child entries are updated as well.

## 7. Permission Management API

### 7.1 Get Permissions

```
GET /repos/:repo/permissions/*path
```

Returns the effective permissions for the authenticated user on the given path, plus the full entry if the user is the owner or a system admin.

**Response:**

```json
{
  "owner": "alice@example.com",
  "default": "read",
  "users": {
    "bob@example.com": "update"
  },
  "effective_level": "read",
  "source": "folder:docs/"
}
```

| Field | Description |
|-------|-------------|
| `owner` | Email of the path owner |
| `default` | Default level for this entry |
| `users` | Per-user overrides (only visible to owner/admin) |
| `effective_level` | The requesting user's resolved permission level |
| `source` | Where the permission was resolved from: `"file"`, `"folder:<path>"`, or `"defaults"` |

### 7.2 Set Permissions

```
PUT /repos/:repo/permissions/*path
```

**Authorization:** Only the path owner or a system admin can modify permissions.

**Request:**

```json
{
  "default": "read",
  "users": {
    "bob@example.com": "update",
    "carol@example.com": "none"
  }
}
```

Setting a user to `"none"` or omitting them removes their entry. The `owner` field cannot be changed via this endpoint.

Changes are committed and pushed to Git.

### 7.3 Remove Permissions

```
DELETE /repos/:repo/permissions/*path
```

**Authorization:** Only the path owner or a system admin.

Removes the entire permission entry for the path. The path then inherits from its parent folder or global defaults.

## 8. Caching & Performance

- The `.wiki-permissions.yaml` file is parsed once and cached in memory per repo as a `Resolver` object.
- The cache is refreshed on: repo clone, repo pull, and any permission-modifying API call.
- Permission resolution is O(d) per check where d is the directory depth (walking parent folders via map lookups). For a repo with 1000 files at average depth 4, listing all files requires ~4000 map lookups — negligible.
- The resolver uses a `sync.RWMutex` for thread-safe access.

## 9. Search Result Filtering

When the search feature is enabled, search results are filtered by the requesting user's `read` permission. Results the user cannot read are excluded from the response.

## 10. Draft Permissions

Draft operations (create, update, publish, delete) require the same permission level as the underlying file operation. Creating or updating a draft requires `update` permission on the target file. Publishing a draft requires `update` permission. Deleting a draft requires `update` permission (since it's modifying draft state, not deleting the actual file).

## 11. Backward Compatibility

- If no `.wiki-permissions.yaml` exists in a repo, all authenticated users have full access (equivalent to current behavior for repos without role restrictions).
- The `public` flag continues to work as before.
- Migration path: after deployment, existing repos work unchanged. Permissions can be gradually introduced by creating `.wiki-permissions.yaml` files.
