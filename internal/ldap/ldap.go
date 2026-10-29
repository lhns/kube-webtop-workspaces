// SPDX-License-Identifier: Apache-2.0

// Package ldap answers "who may use a desktop" from an LDAP group (docs/contract.md, section 6).
package ldap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/lhns/kube-webtop-workspaces/internal/config"
)

// Directory is the membership source.
type Directory interface {
	// Members returns the user IDs in the group. An empty group is an error.
	Members(ctx context.Context) (map[string]struct{}, error)
	// IsMember reports whether one user is in the group.
	IsMember(ctx context.Context, user string) (bool, error)
}

// ErrNoMembers is returned when the group has no members, which is never trusted.
var ErrNoMembers = errors.New("ldap: group has no members")

type client struct{ cfg config.LDAP }

// New returns a Directory backed by go-ldap. Each call opens its own connection.
func New(cfg config.LDAP) Directory { return &client{cfg: cfg} }

// inGroups matches an entry whose memberAttribute names any of the groups (memberOf mode).
func inGroups(c config.LDAP) string {
	var b strings.Builder
	for _, g := range c.GroupDNs() {
		fmt.Fprintf(&b, "(%s=%s)", c.MemberAttribute, goldap.EscapeFilter(g))
	}
	if len(c.GroupDNs()) == 1 {
		return b.String()
	}
	return "(|" + b.String() + ")"
}

// MembersFilter is the search filter listing all members in memberOf mode.
func MembersFilter(c config.LDAP) string {
	return fmt.Sprintf("(&%s%s)", c.UserFilter, inGroups(c))
}

// UserFilter is the filter for one user entry.
func UserFilter(c config.LDAP, user string) string {
	return fmt.Sprintf("(&%s(%s=%s))", c.UserFilter, c.UserAttribute, goldap.EscapeFilter(user))
}

// UserMemberFilter is the filter for one user in memberOf mode.
func UserMemberFilter(c config.LDAP, user string) string {
	return fmt.Sprintf("(&%s(%s=%s)%s)", c.UserFilter, c.UserAttribute, goldap.EscapeFilter(user), inGroups(c))
}

// GroupHasMemberFilter is the base-scope filter on the group entry for one member DN (member mode).
func GroupHasMemberFilter(c config.LDAP, userDN string) string {
	return fmt.Sprintf("(%s=%s)", c.MemberAttribute, goldap.EscapeFilter(userDN))
}

// UserFromDN returns the user ID when the DN's first RDN is the user attribute.
func UserFromDN(dn, userAttr string) (string, bool) {
	parsed, err := goldap.ParseDN(dn)
	if err != nil || len(parsed.RDNs) == 0 {
		return "", false
	}
	for _, a := range parsed.RDNs[0].Attributes {
		if strings.EqualFold(a.Type, userAttr) && a.Value != "" {
			return a.Value, true
		}
	}
	return "", false
}

func (c *client) tlsConfig(host string) (*tls.Config, error) {
	tc := &tls.Config{ServerName: host, InsecureSkipVerify: c.cfg.InsecureSkipVerify, MinVersion: tls.VersionTLS12}
	if c.cfg.CAFile != "" {
		pem, err := os.ReadFile(c.cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("ldap: read caFile: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("ldap: caFile has no certificates")
		}
		tc.RootCAs = pool
	}
	return tc, nil
}

// connect dials, optionally upgrades with StartTLS and binds.
func (c *client) connect(ctx context.Context) (*goldap.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u, err := url.Parse(c.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("ldap: url: %w", err)
	}
	tc, err := c.tlsConfig(u.Hostname())
	if err != nil {
		return nil, err
	}
	timeout := c.cfg.Timeout.Duration
	conn, err := goldap.DialURL(c.cfg.URL, goldap.DialWithTLSConfig(tc), goldap.DialWithDialer(&net.Dialer{Timeout: timeout}))
	if err != nil {
		return nil, fmt.Errorf("ldap: dial: %w", err)
	}
	conn.SetTimeout(timeout)
	if c.cfg.StartTLS && u.Scheme != "ldaps" {
		if err := conn.StartTLS(tc); err != nil {
			conn.Close()
			return nil, fmt.Errorf("ldap: starttls: %w", err)
		}
	}
	if c.cfg.BindDN != "" {
		pw, err := os.ReadFile(c.cfg.BindPasswordFile)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("ldap: read bindPasswordFile: %w", err)
		}
		if err := conn.Bind(c.cfg.BindDN, strings.TrimRight(string(pw), "\r\n")); err != nil {
			conn.Close()
			return nil, fmt.Errorf("ldap: bind: %w", err)
		}
	}
	return conn, nil
}

func (c *client) search(conn *goldap.Conn, base string, scope int, filter string, attrs ...string) ([]*goldap.Entry, error) {
	req := goldap.NewSearchRequest(base, scope, goldap.NeverDerefAliases, 0, int(c.cfg.Timeout.Duration.Seconds()), false, filter, attrs, nil)
	res, err := conn.SearchWithPaging(req, 500)
	if err != nil {
		return nil, fmt.Errorf("ldap: search %q: %w", base, err)
	}
	return res.Entries, nil
}

// userIDs collects the user attribute values of entries.
func (c *client) userIDs(entries []*goldap.Entry) map[string]struct{} {
	out := map[string]struct{}{}
	for _, e := range entries {
		for _, v := range e.GetAttributeValues(c.cfg.UserAttribute) {
			if v != "" {
				out[v] = struct{}{}
			}
		}
	}
	return out
}

func (c *client) Members(ctx context.Context) (map[string]struct{}, error) {
	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	var set map[string]struct{}
	if c.cfg.Membership == config.MembershipMember {
		set, err = c.membersByGroup(conn)
	} else {
		var entries []*goldap.Entry
		entries, err = c.search(conn, c.cfg.BaseDN, goldap.ScopeWholeSubtree, MembersFilter(c.cfg), c.cfg.UserAttribute)
		set = c.userIDs(entries)
	}
	if err != nil {
		return nil, err
	}
	if len(set) == 0 {
		return nil, ErrNoMembers
	}
	return set, nil
}

// membersByGroup reads the group's member DNs and maps each to its user ID.
func (c *client) membersByGroup(conn *goldap.Conn) (map[string]struct{}, error) {
	var groups []*goldap.Entry
	for _, dn := range c.cfg.GroupDNs() {
		es, err := c.search(conn, dn, goldap.ScopeBaseObject, "(objectClass=*)", c.cfg.MemberAttribute)
		if err != nil {
			return nil, err
		}
		groups = append(groups, es...)
	}
	set := map[string]struct{}{}
	for _, g := range groups {
		for _, dn := range g.GetAttributeValues(c.cfg.MemberAttribute) {
			if id, ok := UserFromDN(dn, c.cfg.UserAttribute); ok {
				set[id] = struct{}{}
				continue
			}
			entries, err := c.search(conn, dn, goldap.ScopeBaseObject, c.cfg.UserFilter, c.cfg.UserAttribute)
			if err != nil {
				var le *goldap.Error
				if errors.As(err, &le) && le.ResultCode == goldap.LDAPResultNoSuchObject {
					continue // stale member DN
				}
				return nil, err
			}
			for id := range c.userIDs(entries) {
				set[id] = struct{}{}
			}
		}
	}
	return set, nil
}

func (c *client) IsMember(ctx context.Context, user string) (bool, error) {
	if user == "" {
		return false, nil
	}
	if c.cfg.Membership == config.MembershipMember && c.cfg.BaseDN == "" {
		set, err := c.Members(ctx)
		if err != nil {
			return false, err
		}
		_, ok := set[user]
		return ok, nil
	}
	conn, err := c.connect(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	if c.cfg.Membership != config.MembershipMember {
		entries, err := c.search(conn, c.cfg.BaseDN, goldap.ScopeWholeSubtree, UserMemberFilter(c.cfg, user), c.cfg.UserAttribute)
		if err != nil {
			return false, err
		}
		_, ok := c.userIDs(entries)[user] // directory matching is case-insensitive; the ID is not
		return ok, nil
	}

	users, err := c.search(conn, c.cfg.BaseDN, goldap.ScopeWholeSubtree, UserFilter(c.cfg, user), c.cfg.UserAttribute)
	if err != nil {
		return false, err
	}
	for _, e := range users {
		if _, ok := c.userIDs([]*goldap.Entry{e})[user]; !ok {
			continue
		}
		for _, g := range c.cfg.GroupDNs() {
			groups, err := c.search(conn, g, goldap.ScopeBaseObject, GroupHasMemberFilter(c.cfg, e.DN), "1.1")
			if err != nil {
				return false, err
			}
			if len(groups) > 0 {
				return true, nil
			}
		}
	}
	return false, nil
}
