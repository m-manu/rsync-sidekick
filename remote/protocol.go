package remote

import "github.com/m-manu/rsync-sidekick/entity"

// Message types for the agent protocol (JSON-lines over SSH stdin/stdout).

const (
	MsgWalkRequest     = "walk_request"
	MsgWalkProgress    = "walk_progress"
	MsgWalkResponse    = "walk_response"
	MsgDigestRequest   = "digest_request"
	MsgDigestProgress  = "digest_progress"
	MsgDigestResponse  = "digest_response"
	MsgPerformRequest  = "perform_request"
	MsgPerformResponse = "perform_response"
	MsgQuit            = "quit"
	MsgError           = "error"
)

// Envelope wraps every message.
type Envelope struct {
	Type string `json:"type"`
	// Payload is one of the *Request/*Response structs, encoded as raw JSON.
	Payload []byte `json:"payload,omitempty"`
}

// WalkRequest asks the agent to scan a directory.
type WalkRequest struct {
	DirPath            string   `json:"dir_path"`
	ExcludedNames      []string `json:"excluded_names"`
	ProgressIntervalMs int64    `json:"progress_interval_ms,omitempty"`
}

// WalkProgress is sent by the agent periodically during a directory scan.
type WalkProgress struct {
	FilesFound int `json:"files_found"`
}

// FileMeta mirrors entity.FileMeta for JSON transport.
type FileMeta struct {
	Size              int64 `json:"size"`
	ModifiedTimestamp int64 `json:"modified_timestamp"`
}

// WalkResponse returns the file map and optionally directory timestamps.
type WalkResponse struct {
	Files     map[string]FileMeta `json:"files"`
	Dirs      map[string]int64    `json:"dirs,omitempty"`
	TotalSize int64               `json:"total_size"`
}

// DigestRequest asks the agent to hash a batch of files.
type DigestRequest struct {
	BasePath string   `json:"base_path"`
	Files    []string `json:"files"`
}

// DigestProgress is sent by the agent after each file is hashed.
type DigestProgress struct {
	FilesHashed int `json:"files_hashed"`
	Total       int `json:"total"`
}

// FileDigest mirrors entity.FileDigest for JSON transport.
type FileDigest struct {
	FileExtension string `json:"file_extension"`
	FileSize      int64  `json:"file_size"`
	FileFuzzyHash string `json:"file_fuzzy_hash"`
}

// DigestResponse returns file digests.
type DigestResponse struct {
	Digests map[string]FileDigest `json:"digests"`
}

// ActionSpec describes an action to perform on the remote side.
type ActionSpec struct {
	Type string `json:"type"` // "move", "timestamp", "mkdir", "copy"
	// For move:
	BasePath     string `json:"base_path,omitempty"`
	FromRelPath  string `json:"from_rel_path,omitempty"`
	ToRelPath    string `json:"to_rel_path,omitempty"`
	// For timestamp:
	DestBasePath   string `json:"dest_base_path,omitempty"`
	DestRelPath    string `json:"dest_rel_path,omitempty"`
	ModTimestamp   int64  `json:"mod_timestamp,omitempty"` // unix epoch seconds
	// For mkdir:
	DirPath string `json:"dir_path,omitempty"`
	// For copy:
	FromAbsPath string `json:"from_abs_path,omitempty"`
	ToAbsPath   string `json:"to_abs_path,omitempty"`
	UseReflink  bool   `json:"use_reflink,omitempty"`
}

// PerformRequest asks the agent to execute actions.
type PerformRequest struct {
	Actions []ActionSpec `json:"actions"`
	DryRun  bool         `json:"dry_run"`
}

// ActionResult reports the outcome of a single action.
type ActionResult struct {
	Index   int    `json:"index"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// PerformResponse returns results of the performed actions.
type PerformResponse struct {
	Results []ActionResult `json:"results"`
}

// ErrorResponse returns an error message.
type ErrorResponse struct {
	Message string `json:"message"`
}

// Helper conversions between protocol types and entity types.

func FileMetaFromEntity(fm entity.FileMeta) FileMeta {
	return FileMeta{Size: fm.Size, ModifiedTimestamp: fm.ModifiedTimestamp}
}

func (fm FileMeta) ToEntity() entity.FileMeta {
	return entity.FileMeta{Size: fm.Size, ModifiedTimestamp: fm.ModifiedTimestamp}
}

func FileDigestFromEntity(fd entity.FileDigest) FileDigest {
	return FileDigest{
		FileExtension: fd.FileExtension,
		FileSize:      fd.FileSize,
		FileFuzzyHash: fd.FileFuzzyHash,
	}
}

func (fd FileDigest) ToEntity() entity.FileDigest {
	return entity.FileDigest{
		FileExtension: fd.FileExtension,
		FileSize:      fd.FileSize,
		FileFuzzyHash: fd.FileFuzzyHash,
	}
}
