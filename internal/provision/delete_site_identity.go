package provision

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"os/user"
	"strconv"

	"github.com/lum1t4/wpx/internal/model"
)

// InspectSite never allocates an identity. Matching only a username would risk
// removing an operator-created account after a hash collision or manual reuse;
// the dedicated home and private primary group are part of the contract too.
func (m SystemIdentities) InspectSite(_ context.Context, site model.Site, home string) (Identity, bool, error) {
	if err := model.ValidateSiteID(site.ID); err != nil {
		return Identity{}, false, err
	}
	name := accountName(site.ID)
	account, err := user.Lookup(name)
	if err != nil {
		var unknown user.UnknownUserError
		if errors.As(err, &unknown) {
			return Identity{}, false, nil
		}
		return Identity{}, false, fmt.Errorf("inspect site account: %w", err)
	}
	uid, uidErr := strconv.Atoi(account.Uid)
	gid, gidErr := strconv.Atoi(account.Gid)
	if uidErr != nil || gidErr != nil || uid <= 0 || gid <= 0 || account.Username != name || account.HomeDir != home {
		return Identity{}, false, errors.New("refuse site account with unexpected home or identity")
	}
	group, err := user.LookupGroupId(account.Gid)
	if err != nil || group.Name != name {
		return Identity{}, false, errors.New("refuse site account without its dedicated primary group")
	}
	return Identity{Name: name, UID: uid, GID: gid}, true, nil
}

func (m SystemIdentities) StopSite(ctx context.Context, site model.Site, home string, expected Identity) error {
	if m.Runner == nil {
		return errors.New("site identity command runner is unavailable")
	}
	actual, found, err := m.InspectSite(ctx, site, home)
	if err != nil || !found {
		return err // An absent account is success on an interrupted replay.
	}
	if actual != expected {
		return errors.New("site account changed since deletion began")
	}
	// Graceful runtime shutdown happens first. A site's cron job or lingering
	// PHP request must not keep writing after the tree has been inspected. Only
	// this verified non-root UID is signalled, never a process-name pattern.
	uid := strconv.Itoa(actual.UID)
	if err := m.Runner.Run(ctx, "/usr/bin/pkill", "--signal", "KILL", "--uid", uid); err != nil && !commandExitCode(err, 1) {
		return fmt.Errorf("stop remaining site processes: %w", err)
	}
	if err := m.Runner.Run(ctx, "/usr/bin/pgrep", "--uid", uid); !commandExitCode(err, 1) {
		if err == nil {
			return errors.New("site processes remain; retry deletion after they stop")
		}
		return fmt.Errorf("verify site processes stopped: %w", err)
	}
	return nil
}

func (m SystemIdentities) RemoveSite(ctx context.Context, site model.Site, home string, expected Identity) error {
	actual, found, err := m.InspectSite(ctx, site, home)
	if err != nil || !found {
		return err
	}
	if actual != expected {
		return errors.New("site account changed since deletion began")
	}
	if err := m.StopSite(ctx, site, home, expected); err != nil {
		return err
	}
	// Do not use --remove: userdel's home and mail-spool deletion is less tightly
	// scoped than the explicit, confined tree removal performed by DeleteSite.
	if err := m.Runner.Run(ctx, "/usr/sbin/userdel", "--", actual.Name); err != nil {
		return fmt.Errorf("delete site account: %w", err)
	}
	// A leftover private group (for example because www-data is a member) is
	// harmless and retained. Never edit or delete a group that may have gained
	// another member since provisioning.
	return nil
}

func commandExitCode(err error, code int) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == code
}
