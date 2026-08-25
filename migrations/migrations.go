// Package migrations embeds the ordered SQL schema steps. The runner in
// internal/storage/sqlitedb applies them inside one transaction per step and
// records each step in the schema_migrations ledger.
package migrations

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed *.sql
var files embed.FS

// Step is one versioned migration.
type Step struct {
	Version  int
	Name     string
	Filename string
	Script   string
	Checksum string
}

// Load reads every embedded migration in ascending version order. Filenames must
// follow <version>_<name>.sql with a strictly increasing, gap free version.
func Load() ([]Step, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	steps := make([]Step, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, name, err := parseName(entry.Name())
		if err != nil {
			return nil, err
		}
		body, err := files.ReadFile(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(body)
		steps = append(steps, Step{
			Version:  version,
			Name:     name,
			Filename: entry.Name(),
			Script:   string(body),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Version < steps[j].Version })
	for i, step := range steps {
		if step.Version != i+1 {
			return nil, fmt.Errorf("migration %s breaks the version sequence, expected %d", step.Filename, i+1)
		}
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("no embedded migrations found")
	}
	return steps, nil
}

// LatestVersion reports the highest embedded schema version.
func LatestVersion() (int, error) {
	steps, err := Load()
	if err != nil {
		return 0, err
	}
	return steps[len(steps)-1].Version, nil
}

func parseName(filename string) (int, string, error) {
	trimmed := strings.TrimSuffix(filename, ".sql")
	parts := strings.SplitN(trimmed, "_", 2)
	if len(parts) != 2 || parts[1] == "" {
		return 0, "", fmt.Errorf("migration %s must be named <version>_<name>.sql", filename)
	}
	version, err := strconv.Atoi(parts[0])
	if err != nil || version <= 0 {
		return 0, "", fmt.Errorf("migration %s has an invalid version prefix", filename)
	}
	return version, parts[1], nil
}
