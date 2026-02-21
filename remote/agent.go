package remote

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	set "github.com/deckarep/golang-set/v2"
	"github.com/m-manu/rsync-sidekick/service"
)

// RunAgent reads JSON-line requests from stdin, executes them locally,
// and writes JSON-line responses to stdout. This is invoked on the remote
// side via "rsync-sidekick --agent".
func RunAgent() error {
	reader := bufio.NewReader(os.Stdin)
	writer := os.Stdout

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("agent: read error: %w", err)
		}

		line = []byte(strings.TrimSpace(string(line)))
		if len(line) == 0 {
			continue
		}

		var env Envelope
		if err := json.Unmarshal(line, &env); err != nil {
			writeError(writer, fmt.Sprintf("invalid message: %v", err))
			continue
		}

		switch env.Type {
		case MsgQuit:
			return nil

		case MsgWalkRequest:
			handleWalk(writer, env.Payload)

		case MsgDigestRequest:
			handleDigest(writer, env.Payload)

		case MsgPerformRequest:
			handlePerform(writer, env.Payload)

		default:
			writeError(writer, fmt.Sprintf("unknown message type: %s", env.Type))
		}
	}
}

func handleWalk(w io.Writer, payload []byte) {
	var req WalkRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		writeError(w, fmt.Sprintf("bad walk request: %v", err))
		return
	}

	excluded := set.NewThreadUnsafeSetWithSize[string](len(req.ExcludedNames))
	for _, name := range req.ExcludedNames {
		excluded.Add(name)
	}

	files, totalSize, err := service.FindFilesFromDirectory(req.DirPath, excluded)
	if err != nil {
		writeError(w, fmt.Sprintf("walk failed: %v", err))
		return
	}

	dirs, dirErr := service.FindDirsFromDirectory(req.DirPath, excluded)
	if dirErr != nil {
		writeError(w, fmt.Sprintf("walk dirs failed: %v", dirErr))
		return
	}

	resp := WalkResponse{
		Files:     make(map[string]FileMeta, len(files)),
		Dirs:      dirs,
		TotalSize: totalSize,
	}
	for p, fm := range files {
		resp.Files[p] = FileMetaFromEntity(fm)
	}

	writeResponse(w, MsgWalkResponse, resp)
}

func handleDigest(w io.Writer, payload []byte) {
	var req DigestRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		writeError(w, fmt.Sprintf("bad digest request: %v", err))
		return
	}

	total := len(req.Files)
	resp := DigestResponse{
		Digests: make(map[string]FileDigest, total),
	}

	for i, relPath := range req.Files {
		absPath := filepath.Join(req.BasePath, relPath)
		digest, err := service.GetDigest(absPath)
		if err == nil {
			resp.Digests[relPath] = FileDigestFromEntity(digest)
		}
		writeResponse(w, MsgDigestProgress, DigestProgress{FilesHashed: i + 1, Total: total})
	}

	writeResponse(w, MsgDigestResponse, resp)
}

func handlePerform(w io.Writer, payload []byte) {
	var req PerformRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		writeError(w, fmt.Sprintf("bad perform request: %v", err))
		return
	}

	resp := PerformResponse{
		Results: make([]ActionResult, len(req.Actions)),
	}

	for i, spec := range req.Actions {
		resp.Results[i].Index = i
		if req.DryRun {
			resp.Results[i].Success = true
			continue
		}
		err := executeAction(spec)
		if err != nil {
			resp.Results[i].Success = false
			resp.Results[i].Error = err.Error()
		} else {
			resp.Results[i].Success = true
		}
	}

	writeResponse(w, MsgPerformResponse, resp)
}

func executeAction(spec ActionSpec) error {
	switch spec.Type {
	case "move":
		from := filepath.Join(spec.BasePath, spec.FromRelPath)
		to := filepath.Join(spec.BasePath, spec.ToRelPath)
		if _, err := os.Stat(to); err == nil {
			return fmt.Errorf("file %q already exists", to)
		}
		return os.Rename(from, to)

	case "timestamp":
		dstPath := filepath.Join(spec.DestBasePath, spec.DestRelPath)
		modTime := time.Unix(spec.ModTimestamp, 0)
		return os.Chtimes(dstPath, modTime, modTime)

	case "mkdir":
		return os.MkdirAll(spec.DirPath, os.ModeDir|os.ModePerm)

	case "copy":
		// Create parent directory
		parentDir := filepath.Dir(spec.ToAbsPath)
		if err := os.MkdirAll(parentDir, os.ModeDir|os.ModePerm); err != nil {
			return fmt.Errorf("mkdir for copy failed: %w", err)
		}
		srcInfo, err := os.Stat(spec.FromAbsPath)
		if err != nil {
			return fmt.Errorf("cannot stat source %q: %w", spec.FromAbsPath, err)
		}
		if spec.UseReflink {
			cmd := exec.Command("cp", "--reflink=auto", "-p", spec.FromAbsPath, spec.ToAbsPath)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("reflink copy failed: %w: %s", err, string(out))
			}
		} else {
			in, err := os.Open(spec.FromAbsPath)
			if err != nil {
				return fmt.Errorf("cannot open source %q: %w", spec.FromAbsPath, err)
			}
			out, err := os.Create(spec.ToAbsPath)
			if err != nil {
				in.Close()
				return fmt.Errorf("cannot create destination %q: %w", spec.ToAbsPath, err)
			}
			_, copyErr := io.Copy(out, in)
			in.Close()
			out.Close()
			if copyErr != nil {
				return fmt.Errorf("copy failed: %w", copyErr)
			}
			if err := os.Chmod(spec.ToAbsPath, srcInfo.Mode()); err != nil {
				return fmt.Errorf("chmod failed: %w", err)
			}
		}
		modTime := time.Unix(spec.ModTimestamp, 0)
		return os.Chtimes(spec.ToAbsPath, modTime, modTime)

	default:
		return fmt.Errorf("unknown action type: %s", spec.Type)
	}
}

func writeResponse(w io.Writer, msgType string, payload interface{}) {
	data, _ := json.Marshal(payload)
	env := Envelope{Type: msgType, Payload: data}
	line, _ := json.Marshal(env)
	line = append(line, '\n')
	w.Write(line)
}

func writeError(w io.Writer, msg string) {
	writeResponse(w, MsgError, ErrorResponse{Message: msg})
}
