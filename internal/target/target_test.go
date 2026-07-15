package target

import "testing"

func TestParse(t *testing.T) {
	t.Parallel()
	parsed, err := Parse("lab:/data/project")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "lab" || parsed.Path != "/data/project" {
		t.Fatalf("Parse result = %#v", parsed)
	}
	relative, err := Parse("jump-alias:project/files")
	if err != nil || relative.Path != "project/files" {
		t.Fatalf("relative parse = %#v, %v", relative, err)
	}
	for _, value := range []string{"", "host", ":/path", "host:", "-oProxyCommand=bad:/tmp"} {
		if _, err := Parse(value); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", value)
		}
	}
}
