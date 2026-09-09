package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var ErrMissingToken = errors.New("freebuff auth token not found")

// Credential represents the default account in the Manicode credentials file.
type Credential struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Email           string `json:"email"`
	AuthToken       string `json:"authToken"`
	FingerprintID   string `json:"fingerprintId"`
	FingerprintHash string `json:"fingerprintHash"`
}

// Store defines the interface for loading and saving credentials.
type Store interface {
	Load(ctx context.Context) (Credential, error)
	Save(ctx context.Context, cred Credential) error
	Clear(ctx context.Context) error
}

// FileStore stores credentials in a JSON file.
type FileStore struct {
	Path string
}

type filePayload struct {
	Default Credential `json:"default"`
}

var (
	credCacheMu sync.RWMutex
	credCache   = map[string]struct {
		c   Credential
		err error
		at  time.Time
	}{}
)

// Load reads the default credential from the file with 1s TTL memory cache
// to avoid disk + json per chat request (EnsureActive + Complete both call Load).
func (s FileStore) Load(ctx context.Context) (Credential, error) {
	if err := ctx.Err(); err != nil {
		return Credential{}, err
	}
	credCacheMu.RLock()
	if e, ok := credCache[s.Path]; ok && time.Since(e.at) < time.Second {
		c, err := e.c, e.err
		credCacheMu.RUnlock()
		return c, err
	}
	credCacheMu.RUnlock()

	data, err := os.ReadFile(s.Path)
	if err != nil {
		wrapped := fmt.Errorf("read credentials file: %w", err)
		credCacheMu.Lock()
		credCache[s.Path] = struct {
			c   Credential
			err error
			at  time.Time
		}{Credential{}, wrapped, time.Now()}
		credCacheMu.Unlock()
		return Credential{}, wrapped
	}

	var payload filePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		wrapped := fmt.Errorf("decode credentials file: %w", err)
		credCacheMu.Lock()
		credCache[s.Path] = struct {
			c   Credential
			err error
			at  time.Time
		}{Credential{}, wrapped, time.Now()}
		credCacheMu.Unlock()
		return Credential{}, wrapped
	}

	if payload.Default.AuthToken == "" {
		credCacheMu.Lock()
		credCache[s.Path] = struct {
			c   Credential
			err error
			at  time.Time
		}{Credential{}, ErrMissingToken, time.Now()}
		credCacheMu.Unlock()
		return Credential{}, ErrMissingToken
	}

	credCacheMu.Lock()
	credCache[s.Path] = struct {
		c   Credential
		err error
		at  time.Time
	}{payload.Default, nil, time.Now()}
	credCacheMu.Unlock()
	return payload.Default, nil
}

// Save writes the credential to the file atomically with 0600 permissions.
func (s FileStore) Save(ctx context.Context, cred Credential) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if cred.AuthToken == "" {
		return ErrMissingToken
	}

	dir := filepath.Dir(s.Path)
	if info, err := os.Stat(dir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("credentials directory path is not a directory: %s", dir)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create credentials directory: %w", err)
		}
	} else {
		return fmt.Errorf("stat credentials directory: %w", err)
	}

	data, err := json.MarshalIndent(filePayload{Default: cred}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials file: %w", err)
	}

	if err := writeFileAtomic(s.Path, data); err != nil {
		return err
	}
	credCacheMu.Lock()
	delete(credCache, s.Path)
	credCacheMu.Unlock()
	return nil
}

// Clear removes the credentials file.
func (s FileStore) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := os.Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove credentials file: %w", err)
	}
	credCacheMu.Lock()
	delete(credCache, s.Path)
	credCacheMu.Unlock()
	return nil
}

func writeFileAtomic(path string, data []byte) (err error) {
	tempFile, err := os.CreateTemp(filepath.Dir(path), ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp credentials file: %w", err)
	}

	tempPath := tempFile.Name()
	cleanupTemp := true
	defer func() {
		if !cleanupTemp {
			return
		}
		if removeErr := os.Remove(tempPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			cleanupErr := fmt.Errorf("remove temp credentials file: %w", removeErr)
			if err != nil {
				err = errors.Join(err, cleanupErr)
				return
			}
			err = cleanupErr
		}
	}()

	if err := tempFile.Chmod(0o600); err != nil {
		if closeErr := tempFile.Close(); closeErr != nil {
			return errors.Join(fmt.Errorf("chmod temp credentials file: %w", err), closeErr)
		}
		return fmt.Errorf("chmod temp credentials file: %w", err)
	}

	if _, err := tempFile.Write(data); err != nil {
		if closeErr := tempFile.Close(); closeErr != nil {
			return errors.Join(fmt.Errorf("write temp credentials file: %w", err), closeErr)
		}
		return fmt.Errorf("write temp credentials file: %w", err)
	}

	if err := tempFile.Sync(); err != nil {
		if closeErr := tempFile.Close(); closeErr != nil {
			return errors.Join(fmt.Errorf("sync temp credentials file: %w", err), closeErr)
		}
		return fmt.Errorf("sync temp credentials file: %w", err)
	}

	if err := tempFile.Close(); err != nil {
		return err
	}

	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace credentials file: %w", err)
	}
	cleanupTemp = false

	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod credentials file: %w", err)
	}

	return nil
}
