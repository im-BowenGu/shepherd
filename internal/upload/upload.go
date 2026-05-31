package upload

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
)

type UploadHandler struct {
	userCodePath      string
	userCodeEntrypoint string
	onUpload          func()
}

func NewHandler(userCodePath, entrypoint string, onUpload func()) *UploadHandler {
	return &UploadHandler{
		userCodePath:      userCodePath,
		userCodeEntrypoint: entrypoint,
		onUpload:          onUpload,
	}
}

func (h *UploadHandler) ProcessUpload(file multipart.File, header *multipart.FileHeader) error {
	filename := header.Filename

	if strings.HasSuffix(filename, ".py") {
		return h.processPythonFile(file)
	}

	if strings.HasSuffix(filename, ".zip") {
		return h.processZipFile(file)
	}

	return fmt.Errorf("unsupported file type: %s", filename)
}

func (h *UploadHandler) processPythonFile(file multipart.File) error {
	tempDir, err := os.MkdirTemp("", "shepherd-user-code-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}

	dst := filepath.Join(tempDir, h.userCodeEntrypoint)
	out, err := os.Create(dst)
	if err != nil {
		os.RemoveAll(tempDir)
		return fmt.Errorf("create temp file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, file); err != nil {
		os.RemoveAll(tempDir)
		return fmt.Errorf("copy file: %w", err)
	}
	out.Close()

	return h.swapCodeDir(tempDir)
}

func (h *UploadHandler) processZipFile(file multipart.File) error {
	tempDir, err := os.MkdirTemp("", "shepherd-user-code-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}

	data, err := io.ReadAll(file)
	if err != nil {
		os.RemoveAll(tempDir)
		return fmt.Errorf("read zip: %w", err)
	}

	z, err := zip.NewReader(strings.NewReader(string(data)), int64(len(data)))
	if err != nil {
		os.RemoveAll(tempDir)
		return fmt.Errorf("invalid zip: %w", err)
	}

	for _, f := range z.File {
		if f.FileInfo().IsDir() {
			os.MkdirAll(filepath.Join(tempDir, f.Name), 0755)
			continue
		}
		dst := filepath.Join(tempDir, f.Name)
		os.MkdirAll(filepath.Dir(dst), 0755)
		rc, err := f.Open()
		if err != nil {
			os.RemoveAll(tempDir)
			return fmt.Errorf("extract zip entry %s: %w", f.Name, err)
		}
		out, err := os.Create(dst)
		if err != nil {
			rc.Close()
			os.RemoveAll(tempDir)
			return fmt.Errorf("create %s: %w", dst, err)
		}
		io.Copy(out, rc)
		out.Close()
		rc.Close()
	}

	entrypoint := filepath.Join(tempDir, h.userCodeEntrypoint)
	if _, err := os.Stat(entrypoint); errors.Is(err, os.ErrNotExist) {
		os.RemoveAll(tempDir)
		return fmt.Errorf("zip file must contain a %s file at the root", h.userCodeEntrypoint)
	}

	return h.swapCodeDir(tempDir)
}

func (h *UploadHandler) swapCodeDir(tempDir string) error {
	oldDir := h.userCodePath + ".old"

	if err := os.RemoveAll(oldDir); err != nil {
		log.Printf("Warning: could not remove old backup: %v", err)
	}

	if _, err := os.Stat(h.userCodePath); err == nil {
		if err := os.Rename(h.userCodePath, oldDir); err != nil {
			os.RemoveAll(tempDir)
			return fmt.Errorf("backup old code: %w", err)
		}
	}

	if err := os.Rename(tempDir, h.userCodePath); err != nil {
		os.RemoveAll(oldDir)
		os.Rename(oldDir, h.userCodePath)
		return fmt.Errorf("swap code dir: %w", err)
	}

	os.RemoveAll(oldDir)

	if h.onUpload != nil {
		h.onUpload()
	}

	return nil
}
