package freeze

import (
	"context"
	"strings"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/usersession"
)

// UserCtl is Systemctl over the desktop user's manager.
type UserCtl struct{ User usersession.User }

func (c UserCtl) Units(ctx context.Context) ([]Unit, error) {
	names, err := c.User.ActiveUnits(ctx)
	if err != nil || len(names) == 0 {
		return nil, err
	}
	out, err := c.User.Systemctl(ctx, append([]string{"show", "--property=Id,ControlGroup"}, names...)...)
	if err != nil {
		return nil, err
	}
	return parseShow(string(out)), nil
}

// parseShow reads `systemctl show --property=Id,ControlGroup` blocks (blank-line separated).
func parseShow(out string) []Unit {
	var units []Unit
	for _, block := range strings.Split(out, "\n\n") {
		var u Unit
		for _, l := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(l, "Id="); ok {
				u.Name = strings.TrimSpace(v)
			} else if v, ok := strings.CutPrefix(l, "ControlGroup="); ok {
				u.CGroup = strings.TrimSpace(v)
			}
		}
		if u.Name != "" {
			units = append(units, u)
		}
	}
	return units
}

func (c UserCtl) Freeze(ctx context.Context, units ...string) error {
	_, err := c.User.Systemctl(ctx, append([]string{"freeze"}, units...)...)
	return err
}

func (c UserCtl) Thaw(ctx context.Context, units ...string) error {
	_, err := c.User.Systemctl(ctx, append([]string{"thaw"}, units...)...)
	return err
}
