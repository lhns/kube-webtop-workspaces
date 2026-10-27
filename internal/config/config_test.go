// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

func TestDefaults(t *testing.T) {
	c, err := Parse([]byte("namespace: ws\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Names.Prefix != "desktop-" || c.Desktop.Port != 8080 || c.LDAP.MemberAttribute != "memberOf" ||
		!*c.Controller.RequireAgentReady || len(c.Controller.CleanExitCodes) != 1 || c.Storage.MountPath != "/persist" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.ValidateController() == nil {
		t.Fatal("controller config without image/url must fail")
	}
}

func TestRejects(t *testing.T) {
	for _, in := range []string{
		"namespace: ws\nnames: {prefix: 1x}\n",
		"namespace: ws\nunknownField: 1\n",
		"namespace: ws\nldap: {url: 'ldap://x', groupDN: cn=g}\n",
		"namespace: ws\nldap: {errorPolicy: maybe}\n",
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q) succeeded", in)
		}
	}
}
