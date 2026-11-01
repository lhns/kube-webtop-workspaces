package freeze

import (
	"context"
	"strings"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/sysd"
)

// UserCtl is Systemctl over the desktop user's manager.
type UserCtl struct{ User sysd.User }

func (c UserCtl) Units(ctx context.Context) ([]Unit, error) {
	out, err := c.User.Systemctl(ctx, "list-units", "--plain", "--no-legend", "--no-pager",
		"--type=service,scope", "--state=active")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			names = append(names, f[0])
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	out, err = c.User.Systemctl(ctx, append([]string{"show", "--property=Id,ControlGroup"}, names...)...)
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
