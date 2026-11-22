package sys

import (
	"strings"
	"testing"
)

func TestRenderServerSettings(t *testing.T) {
	got, err := RenderConfig(SambaConfig{
		Server: ServerSettings{NetbiosName: "nas-01", Workgroup: "home", MinProtocol: "SMB3_00",
			MaxProtocol: "SMB3_11", Encryption: "required", Multichannel: "yes"},
		Shares: []ShareSpec{{Name: "media", Path: "/srv/media", Readers: []string{"jay"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := ConfigHeader + `
[global]
   netbios name = NAS-01
   workgroup = HOME
   server min protocol = SMB3_00
   server max protocol = SMB3_11
   server smb encrypt = required
   server multi channel support = yes

[media]
`
	if !strings.HasPrefix(got, want) {
		t.Errorf("config:\n%s\nwant it to start with:\n%s", got, want)
	}
	// The server section is not a share.
	if shares := ParseShares(got); len(shares) != 1 || shares[0].Name != "media" {
		t.Errorf("parsed shares: %+v", shares)
	}

	// Nothing managed: no [global] at all, smb.conf decides.
	plain, _ := RenderConfig(SambaConfig{Shares: []ShareSpec{{Name: "media", Path: "/srv/media", Readers: []string{"jay"}}}})
	if strings.Contains(plain, "[global]") {
		t.Errorf("no server settings, but a [global] section:\n%s", plain)
	}
	// Only what is set is written.
	one, _ := RenderConfig(SambaConfig{Server: ServerSettings{Workgroup: "OFFICE"}})
	if !strings.Contains(one, "workgroup = OFFICE") || strings.Contains(one, "netbios name") || strings.Contains(one, "protocol") {
		t.Errorf("only the workgroup should be set:\n%s", one)
	}
}

// Nothing unchecked reaches the config: a bad setting is refused by the
// system layer itself, whatever the caller sends.
func TestServerSettingsRefuseUnsafeValues(t *testing.T) {
	for name, s := range map[string]ServerSettings{
		"name too long":           {NetbiosName: "a-very-long-server-name"},
		"name with space":         {NetbiosName: "my nas"},
		"name injection":          {NetbiosName: "NAS\n[global]\n   guest ok = yes"},
		"name only digits":        {NetbiosName: "12345"},
		"name ends with dash":     {NetbiosName: "nas-"},
		"workgroup with percent":  {Workgroup: "WG%U"},
		"smb1":                    {MinProtocol: "NT1"},
		"unknown protocol":        {MaxProtocol: "SMB9"},
		"min newer than max":      {MinProtocol: "SMB3_11", MaxProtocol: "SMB2_10"},
		"unknown encryption":      {Encryption: "maybe"},
		"required on smb2":        {Encryption: "required", MinProtocol: "SMB2_10"},
		"multichannel not yes/no": {Multichannel: "on"},
		"protocol with injection": {MinProtocol: "SMB3_00\n   map to guest = Bad User"},
		"encryption with spaces":  {Encryption: "required "},
	} {
		if out, err := RenderConfig(SambaConfig{Server: s}); err == nil {
			t.Errorf("%s: rendered:\n%s", name, out)
		}
	}
	// Required encryption with no minimum set is allowed (Samba's default
	// minimum applies; old clients just cannot connect).
	if _, err := RenderConfig(SambaConfig{Server: ServerSettings{Encryption: "required"}}); err != nil {
		t.Error(err)
	}
}
