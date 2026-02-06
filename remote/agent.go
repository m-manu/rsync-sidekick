package remote

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

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

	resp := WalkResponse{
		Files:     make(map[string]FileMeta, len(files)),
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

	resp := DigestResponse{
		Digests: make(map[string]FileDigest, len(req.Files)),
	}

	var counter int32
	for _, relPath := range req.Files {
		atomic.AddInt32(&counter, 1)
		absPath := filepath.Join(req.BasePath, relPath)
		digest, err := service.GetDigest(absPath)
		if err != nil {
			continue
		}
		resp.Digests[relPath] = FileDigestFromEntity(digest)
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
		srcPath := filepath.Join(spec.SourceBasePath, spec.SourceRelPath)
		dstPath := filepath.Join(spec.DestBasePath, spec.DestRelPath)
		info, err := os.Lstat(srcPath)
		if err != nil {
			return err
		}
		return os.Chtimes(dstPath, info.ModTime(), info.ModTime())

	case "mkdir":
		return os.MkdirAll(spec.DirPath, os.ModeDir|os.ModePerm)

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
