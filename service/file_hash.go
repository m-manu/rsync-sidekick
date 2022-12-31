package service

import (
	"encoding/hex"
	"fmt"
	"github.com/m-manu/rsync-sidekick/bytesutil"
	"github.com/m-manu/rsync-sidekick/entity"
	"github.com/m-manu/rsync-sidekick/lib"
	"hash/crc32"
	"os"
)

const (
	thresholdFileSize = 16 * bytesutil.KIBI
)

// getDigest generates entity.FileDigest of the file provided in an extremely fast manner
// without compromising the quality of uniqueness
func getDigest(path string) (entity.FileDigest, error) {
	info, statErr := os.Lstat(path)
	if statErr != nil {
		return entity.FileDigest{}, statErr
	}
	hash, hashErr := fileHash(path)
	if hashErr != nil {
		return entity.FileDigest{}, hashErr
	}
	return entity.FileDigest{
		FileExtension: lib.GetFileExt(path),
		FileSize:      info.Size(),
		FileFuzzyHash: hash,
	}, nil
}

func fileHash(path string) (string, error) {
	fileInfo, statErr := os.Lstat(path)
	if statErr != nil {
		return "", fmt.Errorf("couldn't stat: %+v", statErr)
	}
	if !fileInfo.Mode().IsRegular() {
		return "", fmt.Errorf("can't compute hash of non-regular file")
	}
	var prefix string
	var bytes []byte
	var fileReadErr error
	if fileInfo.Size() <= thresholdFileSize {
		prefix = "f"
		bytes, fileReadErr = os.ReadFile(path)
	} else {
		prefix = "s"
		bytes, fileReadErr = readCrucialBytes(path, fileInfo.Size())
	}
	if fileReadErr != nil {
		return "", fmt.Errorf("couldn't calculate hash: %+v", fileReadErr)
	}
	h := crc32.NewIEEE()
	_, hashErr := h.Write(bytes)
	if hashErr != nil {
		return "", fmt.Errorf("error while computing hash: %+v", hashErr)
	}
	hash := h.Sum(nil)
	return prefix + hex.EncodeToString(hash), nil
}

func readCrucialBytes(filePath string, fileSize int64) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	firstBytes := make([]byte, thresholdFileSize/2)
	_, fErr := file.ReadAt(firstBytes, 0)
	if fErr != nil {
		return nil, fmt.Errorf("couldn't read first few bytes (maybe file is corrupted?): %+v", fErr)
	}
	middleBytes := make([]byte, thresholdFileSize/4)
	_, mErr := file.ReadAt(middleBytes, fileSize/2)
	if mErr != nil {
		return nil, fmt.Errorf("couldn't read middle bytes (maybe file is corrupted?): %+v", mErr)
	}
	lastBytes := make([]byte, thresholdFileSize/4)
	_, lErr := file.ReadAt(lastBytes, fileSize-thresholdFileSize/4)
	if lErr != nil {
		return nil, fmt.Errorf("couldn't read end bytes (maybe file is corrupted?): %+v", lErr)
	}
	bytes := append(append(firstBytes, middleBytes...), lastBytes...)
	return bytes, nil
}
