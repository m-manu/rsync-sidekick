package remote

import "github.com/m-manu/rsync-sidekick/v2/entity"

// Message types for the agent protocol (JSON-lines over SSH stdin/stdout).

const (
	MsgWalkRequest     = "walk_request"
	MsgWalkProgress    = "walk_progress"
	MsgWalkResponse    = "walk_response"
	MsgWalkChunk       = "walk_chunk"
	MsgDigestRequest   = "digest_request"
	MsgDigestProgress  = "digest_progress"
	MsgDigestResponse  = "digest_response"
	MsgPerformRequest  = "perform_request"
	MsgPerformResponse = "perform_response"
	MsgVersionRequest  = "version_request"
	MsgVersionResponse = "version_response"
	MsgGlobRequest     = "glob_request"
	MsgGlobResponse    = "glob_response"
	MsgQuit            = "quit"
	MsgError           = "error"
)

// VersionRequest asks the agent which version it is. Agents that predate this message
// answer with MsgError, which callers treat as "alive, but ask over a separate ssh call".
type VersionRequest struct{}

// VersionResponse carries the agent's application version, e.g. "v2.1.5".
type VersionResponse struct {
	Version string `json:"version"`
}

// Envelope wraps every message.
type Envelope struct {
	Type string `json:"type"`
	// ID ties a response to its request, which is what allows more than one request to be
	// in flight on the single connection. The agent echoes it on every message it sends
	// for that request. Zero means unset — agents predating this field answer that way,
	// and a client talking to one keeps to a single request at a time.
	ID uint64 `json:"id,omitempty"`
	// Payload is one of the *Request/*Response structs, encoded as raw JSON.
	Payload []byte `json:"payload,omitempty"`
}

// GlobRequest asks the agent which directories match shell wildcard patterns. Agents
// that predate it answer with MsgError ("unknown message type").
type GlobRequest struct {
	Patterns []string `json:"patterns"`
}

// GlobResponse holds the matching directories per pattern, in the order of the request.
type GlobResponse struct {
	Matches [][]string `json:"matches"`
}

// WalkRequest asks the agent to scan a directory.
type WalkRequest struct {
	// MinSize mirrors --min-size so the agent can leave small files out on its side.
	// Clients filter the response as well, so an agent that ignores this field still
	// produces the same result — just with more data on the wire.
	MinSize            int64    `json:"min_size,omitempty"`
	DirPath            string   `json:"dir_path"`
	ExcludedNames      []string `json:"excluded_names"`
	ProgressIntervalMs int64    `json:"progress_interval_ms,omitempty"`
	OneFileSystem      bool     `json:"one_file_system,omitempty"`
	// WalkThreads mirrors --walk-threads; an agent that doesn't know it walks with its default.
	WalkThreads int `json:"walk_threads,omitempty"`
	// ChunkSize asks for the entries in WalkChunk messages of up to this many entries, sent
	// while the walk runs, instead of all of them in the WalkResponse. An agent that
	// doesn't know it answers the old way, which clients still accept.
	ChunkSize int `json:"chunk_size,omitempty"`
}

// WalkChunk carries part of the entries of a walk asked for with ChunkSize.
type WalkChunk struct {
	Entries []WalkEntry `json:"entries"`
}

// WalkEntry is a file or directory of a WalkChunk, with short keys as there are millions.
type WalkEntry struct {
	Path    string `json:"p"`
	Size    int64  `json:"s,omitempty"`
	ModTime int64  `json:"m"`
	IsDir   bool   `json:"d,omitempty"`
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

// WalkResponse returns the file map and optionally directory timestamps. After WalkChunk
// messages it only closes the walk: Files and Dirs stay empty and Entries tells how many
// entries the chunks carried, so a lost chunk can't go unnoticed.
type WalkResponse struct {
	Files     map[string]FileMeta `json:"files"`
	Dirs      map[string]int64    `json:"dirs,omitempty"`
	TotalSize int64               `json:"total_size"`
	Entries   int                 `json:"entries,omitempty"`
	Chunked   bool                `json:"chunked,omitempty"`
}

// DigestRequest asks the agent to hash a batch of files.
type DigestRequest struct {
	BasePath string   `json:"base_path"`
	Files    []string `json:"files"`
	// ProgressIntervalMs throttles DigestProgress messages. Left at zero — as older
	// clients do — the agent falls back to defaultDigestProgressIntervalMs.
	ProgressIntervalMs int64 `json:"progress_interval_ms,omitempty"`
	// DigestCache asks the agent to reuse and record digests in its own cache file.
	// Nil, as sent by older clients, leaves the cache off. An empty Path means the
	// agent's default location.
	DigestCache *DigestCacheSpec `json:"digest_cache,omitempty"`
}

type DigestCacheSpec struct {
	Path string `json:"path,omitempty"`
	// Roots limits which cache entries the agent loads: only those below these paths.
	Roots []string `json:"roots,omitempty"`
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
	BasePath    string `json:"base_path,omitempty"`
	FromRelPath string `json:"from_rel_path,omitempty"`
	ToRelPath   string `json:"to_rel_path,omitempty"`
	// For timestamp:
	DestBasePath string `json:"dest_base_path,omitempty"`
	DestRelPath  string `json:"dest_rel_path,omitempty"`
	ModTimestamp int64  `json:"mod_timestamp,omitempty"` // unix epoch seconds
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
