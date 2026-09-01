package provision

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
)

type DatabaseCredentials struct {
	Name     string `json:"name"`
	User     string `json:"user"`
	Password string `json:"password"`
	Host     string `json:"host"`
}

type SQLExecutor interface {
	Execute(context.Context, string) error
}

type ExecSQL struct{}

func (ExecSQL) Execute(ctx context.Context, statement string) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/mariadb", "--protocol=socket", "--user=root")
	cmd.Stdin = strings.NewReader(statement)
	// SQL can contain generated credentials. Do not attach stdout/stderr or place
	// the statement in argv, where process inspection or logs could disclose it.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("execute MariaDB statement: %w", err)
	}
	return nil
}

type MariaDB struct {
	SecretsRoot string
	SQL         SQLExecutor
}

func (m *MariaDB) Ensure(ctx context.Context, site model.Site) (DatabaseCredentials, error) {
	if m.SQL == nil || !filepath.IsAbs(m.SecretsRoot) || filepath.Clean(m.SecretsRoot) != m.SecretsRoot || m.SecretsRoot == "/" {
		return DatabaseCredentials{}, errors.New("invalid MariaDB manager configuration")
	}
	if err := model.ValidateSiteID(site.ID); err != nil {
		return DatabaseCredentials{}, err
	}
	if err := os.MkdirAll(m.SecretsRoot, 0700); err != nil {
		return DatabaseCredentials{}, fmt.Errorf("prepare database secret directory: %w", err)
	}
	if err := os.Chmod(m.SecretsRoot, 0700); err != nil {
		return DatabaseCredentials{}, err
	}
	path := filepath.Join(m.SecretsRoot, site.ID+".json")
	credentials, err := loadDatabaseCredentials(path)
	if os.IsNotExist(err) {
		credentials, err = newDatabaseCredentials(site.ID)
		if err != nil {
			return DatabaseCredentials{}, err
		}
		encoded, err := json.Marshal(credentials)
		if err != nil {
			return DatabaseCredentials{}, err
		}
		if err := atomicWrite(path, append(encoded, '\n'), 0600); err != nil {
			return DatabaseCredentials{}, fmt.Errorf("persist database credentials: %w", err)
		}
	} else if err != nil {
		return DatabaseCredentials{}, err
	}
	statement := fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n"+
			"CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY '%s';\n"+
			"ALTER USER '%s'@'localhost' IDENTIFIED BY '%s';\n"+
			"GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost';\n",
		credentials.Name, credentials.User, credentials.Password,
		credentials.User, credentials.Password, credentials.Name, credentials.User,
	)
	if err := m.SQL.Execute(ctx, statement); err != nil {
		return DatabaseCredentials{}, err
	}
	return credentials, nil
}

func loadDatabaseCredentials(path string) (DatabaseCredentials, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return DatabaseCredentials{}, err
	}
	var credentials DatabaseCredentials
	if err := json.Unmarshal(content, &credentials); err != nil {
		return DatabaseCredentials{}, fmt.Errorf("decode database credentials: %w", err)
	}
	if credentials.Name == "" || credentials.User == "" || credentials.Password == "" || credentials.Host != "localhost" {
		return DatabaseCredentials{}, errors.New("stored database credentials are invalid")
	}
	return credentials, nil
}

func newDatabaseCredentials(siteID string) (DatabaseCredentials, error) {
	digest := sha256.Sum256([]byte(siteID))
	suffix := hex.EncodeToString(digest[:8])
	secret := make([]byte, 24)
	if _, err := io.ReadFull(rand.Reader, secret); err != nil {
		return DatabaseCredentials{}, err
	}
	return DatabaseCredentials{
		Name: "wpx_" + suffix, User: "wpx_" + suffix,
		Password: base64.RawURLEncoding.EncodeToString(secret), Host: "localhost",
	}, nil
}
