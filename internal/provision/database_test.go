package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type captureSQL struct {
	statements []string
}

func (s *captureSQL) Execute(_ context.Context, statement string) error {
	s.statements = append(s.statements, statement)
	return nil
}

func TestMariaDBCredentialsSurviveRetry(t *testing.T) {
	sql := &captureSQL{}
	database := &MariaDB{SecretsRoot: filepath.Join(t.TempDir(), "secrets"), SQL: sql}
	site := model.Site{ID: "example-com"}
	first, err := database.Ensure(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	second, err := database.Ensure(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(sql.statements) != 2 {
		t.Fatalf("credentials changed across retry: %#v %#v", first, second)
	}
	if strings.Contains(first.Name, "example") || len(first.Password) < 30 {
		t.Fatalf("weak or revealing credentials: %#v", first)
	}
	info, err := os.Stat(filepath.Join(database.SecretsRoot, "example-com.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("secret mode is %o", info.Mode().Perm())
	}
}
