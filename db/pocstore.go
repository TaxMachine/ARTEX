package db

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// PoC package storage: ready-to-run zips the agents assemble per finding,
// stored under <data>/evidence/poc/<finding-id>.zip. The download endpoint
// streams them; poc_package (agent tool) writes them.

// PoCStore resolves and keeps PoC zip paths.
type PoCStore struct {
	dir string
}

// NewPoCStore creates the store rooted at <data>/evidence/poc.
func NewPoCStore(dataDir string) *PoCStore {
	return &PoCStore{dir: filepath.Join(dataDir, "evidence", "poc")}
}

// Path returns the canonical zip path for a finding.
func (s *PoCStore) Path(findingID int64) string {
	return filepath.Join(s.dir, "poc-"+strconvI64(findingID)+".zip")
}

// Save writes the zip bytes and marks the path.
func (s *PoCStore) Save(findingID int64, zipBytes []byte) (string, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", err
	}
	p := s.Path(findingID)
	if err := os.WriteFile(p, zipBytes, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// Open streams an existing package.
func (s *PoCStore) Open(findingID int64) (*os.File, error) {
	return os.Open(s.Path(findingID))
}

func strconvI64(v int64) string { return strconv.FormatInt(v, 10) }

var _ = time.Now
