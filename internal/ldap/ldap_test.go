// SPDX-License-Identifier: Apache-2.0

package ldap

import (
	"testing"

	"github.com/lhns/kube-webtop-workspaces/internal/config"
)

func cfg() config.LDAP {
	return config.LDAP{
		UserFilter: "(objectClass=person)", UserAttribute: "uid",
		GroupDN: "cn=webtop,ou=groups,dc=example,dc=org", MemberAttribute: "memberOf",
	}
}

func TestFilters(t *testing.T) {
	c := cfg()
	if got, want := MembersFilter(c), "(&(objectClass=person)(memberOf=cn=webtop,ou=groups,dc=example,dc=org))"; got != want {
		t.Errorf("members: %s", got)
	}
	if got, want := UserFilter(c, "alice"), "(&(objectClass=person)(uid=alice))"; got != want {
		t.Errorf("user: %s", got)
	}
	if got, want := UserMemberFilter(c, "alice"),
		"(&(objectClass=person)(uid=alice)(memberOf=cn=webtop,ou=groups,dc=example,dc=org))"; got != want {
		t.Errorf("user+member: %s", got)
	}
	c.MemberAttribute = "member"
	if got, want := GroupHasMemberFilter(c, "uid=a,dc=x"), "(member=uid=a,dc=x)"; got != want {
		t.Errorf("group has member: %s", got)
	}
}

func TestFiltersAdminGroup(t *testing.T) {
	c := cfg()
	c.AdminGroupDN = "cn=admin,ou=groups,dc=example,dc=org"
	if got, want := MembersFilter(c), "(&(objectClass=person)(|(memberOf=cn=webtop,ou=groups,dc=example,dc=org)(memberOf=cn=admin,ou=groups,dc=example,dc=org)))"; got != want {
		t.Errorf("members: %s", got)
	}
	if got, want := UserMemberFilter(c, "alice"), "(&(objectClass=person)(uid=alice)(|(memberOf=cn=webtop,ou=groups,dc=example,dc=org)(memberOf=cn=admin,ou=groups,dc=example,dc=org)))"; got != want {
		t.Errorf("user+member: %s", got)
	}
}

func TestFiltersEscape(t *testing.T) {
	c := cfg()
	if got, want := UserFilter(c, "a)(uid=*"), `(&(objectClass=person)(uid=a\29\28uid=\2a))`; got != want {
		t.Errorf("user escape: %s", got)
	}
	if got, want := GroupHasMemberFilter(c, "cn=a*)(x"), `(memberOf=cn=a\2a\29\28x)`; got != want {
		t.Errorf("dn escape: %s", got)
	}
}

func TestUserFromDN(t *testing.T) {
	for _, tc := range []struct {
		dn, attr, want string
		ok             bool
	}{
		{"uid=alice,ou=people,dc=x", "uid", "alice", true},
		{"UID=Alice,ou=people,dc=x", "uid", "Alice", true},
		{"cn=Alice,ou=people,dc=x", "uid", "", false},
		{"not a dn", "uid", "", false},
	} {
		got, ok := UserFromDN(tc.dn, tc.attr)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got %q,%v", tc.dn, got, ok)
		}
	}
}
