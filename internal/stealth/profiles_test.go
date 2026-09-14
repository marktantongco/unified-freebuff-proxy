package stealth

import (
	"testing"

	utls "github.com/refraction-networking/utls"
)

func TestProfileRegistryComplete(t *testing.T) {
	seen := map[ProfileID]bool{}
	for _, p := range RotatableProfiles() {
		if p.UserAgent == "" {
			t.Errorf("%s: empty UserAgent", p.ID)
		}
		if p.ClientHelloID.Client == "" {
			t.Errorf("%s: empty ClientHelloID.Client", p.ID)
		}
		if seen[p.ID] {
			t.Errorf("duplicate profile %s", p.ID)
		}
		seen[p.ID] = true
	}
	if len(seen) < 10 {
		t.Fatalf("want >=10 rotatable profiles, got %d", len(seen))
	}
}

func TestProfileByName(t *testing.T) {
	cases := map[string]ProfileID{
		"":            ProfileIDChrome120,
		"chrome131":   ProfileIDChrome131,
		"chrome133":   ProfileIDChrome133,
		"edge106":     ProfileIDEdge106,
		"safari17":    ProfileIDSafari17,
		"safari16":    ProfileIDSafari16,
		"firefox120":  ProfileIDFirefox120,
		"firefox105":  ProfileIDFirefox105,
		"firefox102":  ProfileIDFirefox102,
		"ios":         ProfileIDIOSAuto,
		"android":     ProfileIDAndroid,
		"no-such-one": ProfileIDChrome120, // unknown → default
	}
	for name, want := range cases {
		if got := ProfileByName(name); got.ID != want {
			t.Errorf("ByName(%q) = %s, want %s", name, got.ID, want)
		}
	}
	if got := ProfileByName("random"); got.ID == "" {
		t.Error("ByName(random) returned empty profile")
	}
	// utls parrot IDs must resolve to real specs (non-nil, non-panic).
	for _, p := range RotatableProfiles() {
		if p.ClientHelloID == utls.HelloCustom && p.CustomSpec == nil {
			t.Errorf("%s: HelloCustom without CustomSpec", p.ID)
		}
	}
}

func TestRotateProfileCycles(t *testing.T) {
	n := len(RotatableProfiles())
	seen := map[ProfileID]int{}
	for i := 0; i < n; i++ {
		seen[RotateProfile().ID]++
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("rotation hit %s %d times in one cycle, want 1", id, count)
		}
	}
}

func TestRandomProfileMember(t *testing.T) {
	members := map[ProfileID]bool{}
	for _, p := range RotatableProfiles() {
		members[p.ID] = true
	}
	for i := 0; i < 50; i++ {
		if !members[RandomProfile().ID] {
			t.Fatal("RandomProfile returned non-member")
		}
	}
}
