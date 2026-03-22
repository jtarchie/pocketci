package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"

	"github.com/georgysavva/scany/v2/sqlscan"
	"github.com/jtarchie/pocketci/secrets"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// Config holds the configuration for the SQLite secrets backend.
type Config struct {
	Path       string // database file path, or ":memory:" for in-memory
	Passphrase string // encryption passphrase for application-layer AES-256-GCM
}

// SQLite is a secrets backend that stores encrypted secrets in SQLite.
type SQLite struct {
	db        *sql.DB
	encryptor *secrets.Encryptor
	logger    *slog.Logger
}

// New creates a new SQLite secrets manager.
func New(cfg Config, logger *slog.Logger) (secrets.Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}

	logger = logger.WithGroup("secrets.sqlite")

	dbPath := cfg.Path
	if dbPath == "" {
		dbPath = ":memory:"
	}

	passphrase := cfg.Passphrase
	if passphrase == "" {
		return nil, errors.New("sqlite secrets backend requires a non-empty Passphrase")
	}

	key := secrets.DeriveKey(passphrase)

	encryptor, err := secrets.NewEncryptor(key)
	if err != nil {
		return nil, fmt.Errorf("could not create encryptor: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("could not open secrets database: %w", err)
	}

	//nolint: noctx
	_, err = db.Exec(schema)
	if err != nil {
		return nil, fmt.Errorf("could not create secrets table: %w", err)
	}

	db.SetMaxIdleConns(1)
	db.SetMaxOpenConns(1)

	logger.Info("secrets.sqlite.initialized", "db", dbPath)

	return &SQLite{
		db:        db,
		encryptor: encryptor,
		logger:    logger,
	}, nil
}

func (s *SQLite) Get(ctx context.Context, scope string, key string) (string, error) {
	var encryptedValue []byte

	err := sqlscan.Get(ctx, s.db, &encryptedValue, `
		SELECT encrypted_value FROM secrets WHERE scope = ? AND key = ?
	`, scope, key)
	if err != nil {
		if sqlscan.NotFound(err) {
			return "", secrets.ErrNotFound
		}

		return "", fmt.Errorf("could not query secret: %w", err)
	}

	plaintext, err := s.encryptor.Decrypt(encryptedValue)
	if err != nil {
		return "", fmt.Errorf("could not decrypt secret %q in scope %q: %w", key, scope, err)
	}

	return string(plaintext), nil
}

func (s *SQLite) Set(ctx context.Context, scope string, key string, value string) error {
	encrypted, err := s.encryptor.Encrypt([]byte(value))
	if err != nil {
		return fmt.Errorf("could not encrypt secret: %w", err)
	}

	// Version and updated_at are managed automatically:
	// INSERT starts at version=1; the secrets_version_update trigger increments
	// version and refreshes updated_at on every subsequent UPDATE.
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO secrets (scope, key, encrypted_value)
		VALUES (?, ?, ?)
		ON CONFLICT(scope, key) DO UPDATE SET
			encrypted_value = excluded.encrypted_value
	`, scope, key, encrypted)
	if err != nil {
		return fmt.Errorf("could not store secret: %w", err)
	}

	s.logger.Info("secret.set", "scope", scope, "key", key)

	return nil
}

func (s *SQLite) Delete(ctx context.Context, scope string, key string) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM secrets WHERE scope = ? AND key = ?
	`, scope, key)
	if err != nil {
		return fmt.Errorf("could not delete secret: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("could not check delete result: %w", err)
	}

	if rows == 0 {
		return secrets.ErrNotFound
	}

	s.logger.Info("secret.deleted", "scope", scope, "key", key)

	return nil
}

func (s *SQLite) ListByScope(ctx context.Context, scope string) ([]string, error) {
	var keys []string

	err := sqlscan.Select(ctx, s.db, &keys, `
		SELECT key FROM secrets WHERE scope = ? ORDER BY key
	`, scope)
	if err != nil {
		return nil, fmt.Errorf("could not list secrets by scope: %w", err)
	}

	return keys, nil
}

func (s *SQLite) DeleteByScope(ctx context.Context, scope string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM secrets WHERE scope = ?
	`, scope)
	if err != nil {
		return fmt.Errorf("could not delete secrets by scope: %w", err)
	}

	s.logger.Info("secrets.deleted_by_scope", "scope", scope)

	return nil
}

func (s *SQLite) Close() error {
	return s.db.Close()
}
