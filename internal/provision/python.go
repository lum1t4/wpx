package provision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
)

// SystemPython creates a site-local virtual environment that can see Ubuntu's
// maintained Gunicorn package. A systemd socket starts workers only on traffic,
// so an idle Python site has no resident application process.
type SystemPython struct {
	Runner   Runner
	UnitRoot string
	RunRoot  string
}

func (p *SystemPython) Ensure(ctx context.Context, site model.Site, identity Identity, siteDir, publicDir string) (string, error) {
	if p.Runner == nil || !filepath.IsAbs(p.UnitRoot) || !filepath.IsAbs(p.RunRoot) {
		return "", errors.New("invalid Python runtime configuration")
	}
	venv := filepath.Join(siteDir, "venv")
	python := filepath.Join(venv, "bin", "python")
	if _, err := os.Stat(python); os.IsNotExist(err) {
		if err := p.Runner.Run(ctx, "/usr/sbin/runuser", "--user", identity.Name, "--", "/usr/bin/python3", "-m", "venv", "--system-site-packages", venv); err != nil {
			return "", fmt.Errorf("create Python virtual environment: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("inspect Python virtual environment: %w", err)
	}
	if err := ensurePythonWelcomePage(publicDir, identity); err != nil {
		return "", err
	}
	if err := os.MkdirAll(p.UnitRoot, 0755); err != nil {
		return "", err
	}
	socket := filepath.Join(p.RunRoot, site.ID+".sock")
	unitBase := "wpx-python-" + site.ID
	socketPath := filepath.Join(p.UnitRoot, unitBase+".socket")
	servicePath := filepath.Join(p.UnitRoot, unitBase+".service")
	if err := writeManagedUnit(socketPath, renderPythonSocket(site, identity, socket)); err != nil {
		return "", err
	}
	if err := writeManagedUnit(servicePath, renderPythonService(site, identity, publicDir, python)); err != nil {
		return "", err
	}
	if err := p.Runner.Run(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
		return "", fmt.Errorf("reload systemd units: %w", err)
	}
	if err := p.Runner.Run(ctx, "/usr/bin/systemctl", "enable", "--now", unitBase+".socket"); err != nil {
		return "", fmt.Errorf("start Python socket: %w", err)
	}
	return socket, nil
}

func ensurePythonWelcomePage(publicDir string, identity Identity) error {
	path := filepath.Join(publicDir, "app.py")
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect Python entry point: %w", err)
	}
	content := []byte("def application(environ, start_response):\n    body = b\"Python site ready\\n\"\n    start_response(\"200 OK\", [(\"Content-Type\", \"text/plain; charset=utf-8\"), (\"Content-Length\", str(len(body)))])\n    return [body]\n")
	if err := atomicWrite(path, content, 0640); err != nil {
		return fmt.Errorf("write Python entry point: %w", err)
	}
	return os.Chown(path, identity.UID, identity.GID)
}

func renderPythonSocket(site model.Site, identity Identity, socket string) string {
	return ownershipMarker + `[Unit]
Description=WPX Python socket for ` + site.ID + `

[Socket]
ListenStream=` + socket + `
SocketUser=` + identity.Name + `
SocketGroup=www-data
SocketMode=0660
DirectoryMode=0755
RemoveOnStop=true

[Install]
WantedBy=sockets.target
`
}

func renderPythonService(site model.Site, identity Identity, publicDir, python string) string {
	return ownershipMarker + `[Unit]
Description=WPX Python application for ` + site.ID + `
Requires=wpx-python-` + site.ID + `.socket
After=network.target

[Service]
Type=simple
User=` + identity.Name + `
Group=` + identity.Name + `
WorkingDirectory=` + publicDir + `
ExecStart=` + python + ` -m gunicorn --workers 2 --bind fd://3 app:application
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=` + filepath.Dir(publicDir) + `
Restart=on-failure
RestartSec=2s
`
}

func writeManagedUnit(path, content string) error {
	previous, exists, err := managedFileState(path)
	if err != nil {
		return fmt.Errorf("inspect managed systemd unit: %w", err)
	}
	_ = previous
	_ = exists
	if !strings.HasPrefix(content, ownershipMarker) {
		return errors.New("generated systemd unit lacks ownership marker")
	}
	if err := atomicWrite(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}
	return nil
}
