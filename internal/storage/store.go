// Package storage owns the on-disk layout of the Coordinator.
//
// Layout (all under one data directory, never on a network filesystem):
//
//	inputs/<job_id>/input.bin          published atomically from tmp/
//	inputs/<job_id>/meta.json
//	results/<job_id>/result.{md,txt,json}
//	results/<job_id>/manifest.json
//	work/<job_id>/                      worker scratch (worker-local, not here)
//	tmp/                                *.part staging area
//
// Every write goes to tmp/, is fsynced, hashed and then renamed. Directory
// deletion is idempotent so a crash mid-cleanup can be retried forever.
package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// ErrTooLarge is returned when a stream exceeds the configured limit.
var ErrTooLarge = errors.New("stream exceeds limit")

// ErrQuota is returned when the data filesystem is below the safety watermark.
var ErrQuota = errors.New("insufficient storage")

// Store is the Coordinator file store.
type Store struct {
	Root       string
	MinFree    int64 // refuse writes below this many free bytes
	TotalQuota int64 // 0 = unlimited
}

// invalidPathComponent replaces a job id or result version that is not a single safe path
// component. It contains a NUL byte, which every file system call rejects with EINVAL, so a
// path helper handed "..", "" or "a/b" yields a path that cannot be opened, written or
// removed - instead of one that escapes inputs/ or results/.
const invalidPathComponent = "\x00invalid"

// ValidJobID reports whether id is safe to use as a directory name under the data root:
// non-empty, at most 128 bytes, ASCII letters, digits, '_' and '-' only, so it can never be
// "." or "..", contain a separator, or be read as an option. Job ids are "job_" plus
// Crockford base32 (jobs.NewJobID), well inside this set.
func ValidJobID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		case c == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}

// safeJobID returns id when it is valid, otherwise a component no syscall accepts.
func safeJobID(id string) string {
	if ValidJobID(id) {
		return id
	}
	return invalidPathComponent
}

// safeVersion returns the directory name of a result version ("sha256:<hex>" or bare
// hex), or a component no syscall accepts when it could leave the job's directory. An
// empty version keeps its old meaning (the job's result directory itself).
func safeVersion(version string) string {
	v := strings.TrimPrefix(version, "sha256:")
	if v != "" && !ValidJobID(v) {
		return invalidPathComponent
	}
	return v
}

// Init creates the directory tree.
func (s *Store) Init() error {
	for _, d := range []string{"", "inputs", "results", "tmp", "quarantine"} {
		p := filepath.Join(s.Root, d)
		if err := os.MkdirAll(p, 0o750); err != nil {
			return fmt.Errorf("mkdir %s: %w", p, err)
		}
	}
	return nil
}

// Path helpers. An invalid job id never yields a usable path (see safeJobID).
func (s *Store) InputDir(jobID string) string {
	return filepath.Join(s.Root, "inputs", safeJobID(jobID))
}
func (s *Store) InputPath(jobID string) string { return filepath.Join(s.InputDir(jobID), "input.bin") }
func (s *Store) MetaPath(jobID string) string  { return filepath.Join(s.InputDir(jobID), "meta.json") }
func (s *Store) ResultDir(jobID string) string {
	return filepath.Join(s.Root, "results", safeJobID(jobID))
}

// ResultDirAt is the directory of one published result generation. Results are
// content-addressed by their manifest version, so two workers that race to finish the same
// job write to different directories and the loser can be removed without touching the
// winner's files. A retried attempt that produces identical bytes shares the directory,
// which is harmless (same content).
func (s *Store) ResultDirAt(jobID, version string) string {
	return filepath.Join(s.ResultDir(jobID), safeVersion(version))
}

// ResultDirFor returns the directory holding the job's committed generation. Installations
// upgraded from the pre-generation layout still have manifest.json directly under
// results/<job>/; it is used when its version matches, so an upgrade does not turn every
// existing result into a 410.
func (s *Store) ResultDirFor(jobID, version string) string {
	dir := s.ResultDirAt(jobID, version)
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err == nil {
		return dir
	}
	legacy := s.ResultDir(jobID)
	if b, err := os.ReadFile(filepath.Join(legacy, "manifest.json")); err == nil {
		var m Manifest
		if json.Unmarshal(b, &m) == nil && m.Version == version {
			return legacy
		}
	}
	return dir
}

// ResultPath is the legacy job-wide path. Nothing publishes here any more; it is kept for
// callers that only need the parent directory.
func (s *Store) ResultPath(jobID, name string) string {
	return filepath.Join(s.ResultDir(jobID), name)
}

// FreeBytes reports free space on the data filesystem.
func (s *Store) FreeBytes() (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.Root, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// CheckSpace verifies the safety watermark plus an optional additional need.
func (s *Store) CheckSpace(need int64) error {
	free, err := s.FreeBytes()
	if err != nil {
		return err
	}
	if free-need < s.MinFree {
		return ErrQuota
	}
	return nil
}

// Usage sums bytes under inputs/ and results/ (best effort, used by /readyz).
func (s *Store) Usage() (int64, error) {
	var total int64
	for _, d := range []string{"inputs", "results", "tmp"} {
		err := filepath.Walk(filepath.Join(s.Root, d), func(_ string, info os.FileInfo, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if !info.IsDir() {
				total += info.Size()
			}
			return nil
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// InputWriter streams an upload into tmp/, enforcing size and hashing.
type InputWriter struct {
	f    *os.File
	part string
	h    hash.Hash
	n    int64
	max  int64
}

// NewInputWriter starts a staged upload for jobID.
func (s *Store) NewInputWriter(jobID string, maxBytes int64) (*InputWriter, error) {
	if !ValidJobID(jobID) {
		return nil, fmt.Errorf("invalid job id %q", jobID)
	}
	if err := s.CheckSpace(0); err != nil {
		return nil, err
	}
	tmp := filepath.Join(s.Root, "tmp", jobID+".input.part")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create staging file: %w", err)
	}
	return &InputWriter{f: f, part: tmp, h: sha256.New(), max: maxBytes}, nil
}

// Write implements io.Writer with a hard size limit.
func (w *InputWriter) Write(p []byte) (int, error) {
	if w.max > 0 && w.n+int64(len(p)) > w.max {
		return 0, ErrTooLarge
	}
	n, err := w.f.Write(p)
	w.n += int64(n)
	if n > 0 {
		w.h.Write(p[:n])
	}
	return n, err
}

// Abort discards the staged file.
func (w *InputWriter) Abort() {
	_ = w.f.Close()
	_ = os.Remove(w.part)
}

// Size returns bytes written so far.
func (w *InputWriter) Size() int64 { return w.n }

// SHA256 returns the hex digest of the bytes written so far, without ending the upload.
func (w *InputWriter) SHA256() string { return hex.EncodeToString(w.h.Sum(nil)) }

// Path is the staging file the upload is being written to (valid until Commit/Abort).
func (w *InputWriter) Path() string { return w.part }

// Commit fsyncs, renames into place and returns size and sha256.
func (s *Store) CommitInput(w *InputWriter, jobID string, meta any) (int64, string, error) {
	if err := w.f.Sync(); err != nil {
		w.Abort()
		return 0, "", err
	}
	if err := w.f.Close(); err != nil {
		w.Abort()
		return 0, "", err
	}
	sum := hex.EncodeToString(w.h.Sum(nil))
	if err := os.MkdirAll(s.InputDir(jobID), 0o750); err != nil {
		_ = os.Remove(w.part)
		return 0, "", err
	}
	if err := os.Rename(w.part, s.InputPath(jobID)); err != nil {
		_ = os.Remove(w.part)
		return 0, "", err
	}
	if meta != nil {
		b, _ := json.MarshalIndent(meta, "", "  ")
		if err := s.writeAtomic(s.MetaPath(jobID), b); err != nil {
			return 0, "", err
		}
	}
	// A rename is only durable once the containing directory is synced; otherwise the
	// database could durably reference an input whose directory entry was lost.
	syncDir(s.InputDir(jobID))
	return w.n, sum, nil
}

// OpenInput opens the published input for reading.
func (s *Store) OpenInput(jobID string) (*os.File, os.FileInfo, error) {
	f, err := os.Open(s.InputPath(jobID))
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, st, nil
}

// Artifact describes one published result file.
type Artifact struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest is written next to the result files.
type Manifest struct {
	JobID     string     `json:"job_id"`
	Version   string     `json:"result_version"`
	Engine    string     `json:"engine"`
	EngineVer string     `json:"engine_version"`
	CreatedAt time.Time  `json:"created_at"`
	Files     []Artifact `json:"files"`
}

// CommitResult publishes result files atomically.
//
// Files are staged as tmp/<job>.result.<name>.part, fsynced, renamed, and only
// then the manifest is written. A missing manifest therefore means "not
// committed" and the Coordinator never marks such a job completed.
func (s *Store) CommitResult(jobID, engine, engineVer string, files map[string]io.Reader) (*Manifest, error) {
	// Stage inside the job's result directory (same filesystem, so the final renames are
	// atomic) but under a private name: the destination is only known once the content
	// hashes are computed.
	stage := filepath.Join(s.ResultDir(jobID), ".stage-"+stageToken())
	dir := stage
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	manifest := &Manifest{JobID: jobID, Engine: engine, EngineVer: engineVer, CreatedAt: time.Now().UTC()}
	names := make([]string, 0, len(files))
	for n := range files {
		if strings.ContainsAny(n, "/\\") || n == "" {
			return nil, fmt.Errorf("invalid artifact name %q", n)
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		// Ingest straight into the private staging directory: the old shared
		// tmp/<job>.result.<name>.part name let two concurrent attempts truncate each other's
		// file, and cleanupParts(jobID) then deleted files the other attempt was still writing.
		if err := ingestFile(filepath.Join(dir, name), files[name]); err != nil {
			_ = os.RemoveAll(stage)
			return nil, err
		}
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			_ = os.RemoveAll(stage)
			return nil, err
		}
		art := Artifact{Name: name, Size: st.Size()}
		if art.SHA256, err = hashFile(filepath.Join(dir, name)); err != nil {
			_ = os.RemoveAll(stage)
			return nil, err
		}
		manifest.Files = append(manifest.Files, art)
	}
	manifest.Version = manifestVersion(manifest)
	b, _ := json.MarshalIndent(manifest, "", "  ")
	if err := s.writeAtomic(filepath.Join(dir, "manifest.json"), b); err != nil {
		_ = os.RemoveAll(stage)
		return nil, err
	}
	final := s.ResultDirAt(jobID, manifest.Version)
	if err := os.MkdirAll(final, 0o750); err != nil {
		_ = os.RemoveAll(stage)
		return nil, err
	}
	// Move each staged file into the content-addressed directory. An identical concurrent
	// publication writes identical bytes, so a rename over it is a no-op in effect.
	for _, a := range manifest.Files {
		if err := os.Rename(filepath.Join(stage, a.Name), filepath.Join(final, a.Name)); err != nil {
			_ = os.RemoveAll(stage)
			return nil, err
		}
	}
	if err := os.Rename(filepath.Join(stage, "manifest.json"), filepath.Join(final, "manifest.json")); err != nil {
		_ = os.RemoveAll(stage)
		return nil, err
	}
	syncDir(final)
	syncDir(s.ResultDir(jobID))
	_ = os.RemoveAll(stage)
	return manifest, nil
}

// PruneResults removes result directories of a job other than keep (including leftovers from
// an interrupted publication). Called after a generation is committed: the others are
// unreachable, and leaving them would make disk usage exceed the storage accounting.
func (s *Store) PruneResults(jobID, keep string) error {
	entries, err := os.ReadDir(s.ResultDir(jobID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	keepName := strings.TrimPrefix(keep, "sha256:")
	var firstErr error
	for _, e := range entries {
		if !e.IsDir() || e.Name() == keepName {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.ResultDir(jobID), e.Name())); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ReadManifestAt loads the manifest of one result generation.
func (s *Store) ReadManifestAt(jobID, version string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(s.ResultDirFor(jobID, version), "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func manifestVersion(m *Manifest) string {
	h := sha256.New()
	for _, f := range m.Files {
		fmt.Fprintf(h, "%s:%d:%s\n", f.Name, f.Size, f.SHA256)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// DeleteJob removes all input and result files for a job. Idempotent: a
// missing directory is not an error, and a partial removal can be retried.
func (s *Store) DeleteJob(jobID string) error {
	if !ValidJobID(jobID) {
		return fmt.Errorf("invalid job id %q", jobID)
	}
	var firstErr error
	for _, d := range []string{s.InputDir(jobID), s.ResultDir(jobID)} {
		if err := os.RemoveAll(d); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	s.cleanupParts(jobID)
	return firstErr
}

// stageToken returns a short random suffix for a staging directory.
func stageToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// syncDir fsyncs a directory so a rename inside it survives a power loss. Renaming a file is
// only durable once the containing directory is synced; without this the database could
// durably reference a file whose directory entry was lost.
func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}

// SweepParts removes stale *.part files left behind by crashes.
func (s *Store) SweepParts(olderThan time.Duration) (int, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "tmp"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := time.Now().Add(-olderThan)
	n := 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(s.Root, "tmp", e.Name())); err == nil {
				n++
			}
		}
	}
	return n, nil
}

func (s *Store) cleanupParts(jobID string) {
	matches, _ := filepath.Glob(filepath.Join(s.Root, "tmp", jobID+"*.part"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

func (s *Store) writeAtomic(dst string, data []byte) error {
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	syncDir(filepath.Dir(dst))
	return nil
}

func ingestFile(dst string, r io.Reader) error {
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	syncDir(filepath.Dir(dst))
	return nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashFile exposes hashing for callers that verify transfers.
func HashFile(p string) (string, error) { return hashFile(p) }
