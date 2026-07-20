package job

import "testing"

func TestParseContainerIDFromMountinfo(t *testing.T) {
	const compose = `650 630 0:64 / /proc rw,nosuid shared:266 - proc proc rw
660 630 259:1 /var/lib/docker/containers/3f2a1b9c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8/hostname /etc/hostname rw,relatime shared:1 - ext4 /dev/root rw
661 630 259:1 /var/lib/docker/overlay2/abcdef0123456789/merged /etc/other rw - ext4 /dev/root rw`
	got := parseContainerIDFromMountinfo(compose)
	want := "3f2a1b9c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8"
	if got != want {
		t.Fatalf("mountinfo id = %q, want %q", got, want)
	}
	if got := parseContainerIDFromMountinfo("no containers path here\n/overlay2/deadbeef"); got != "" {
		t.Fatalf("expected empty for non-container mountinfo, got %q", got)
	}
}

func TestParseContainerIDFromCgroup(t *testing.T) {
	const v1 = `12:pids:/docker/aa11bb22cc33dd44ee55ff6677889900aabbccddeeff00112233445566778899
11:memory:/docker/aa11bb22cc33dd44ee55ff6677889900aabbccddeeff00112233445566778899`
	want := "aa11bb22cc33dd44ee55ff6677889900aabbccddeeff00112233445566778899"
	if got := parseContainerIDFromCgroup(v1); got != want {
		t.Fatalf("cgroup id = %q, want %q", got, want)
	}
	if got := parseContainerIDFromCgroup("0::/\n0::/init.scope"); got != "" {
		t.Fatalf("expected empty for host cgroup, got %q", got)
	}
}

func TestParseContainerIDFromHostname(t *testing.T) {
	cases := map[string]string{
		"abcdef123456": "abcdef123456", // 12-hex docker run default
		"dockbrr":      "",             // compose service name
		"":             "",
		"abcdef12345g": "",             // non-hex char
		"abcdef1234567": "",            // 13 chars
	}
	for in, want := range cases {
		if got := parseContainerIDFromHostname(in); got != want {
			t.Errorf("hostname(%q) = %q, want %q", in, got, want)
		}
	}
}
