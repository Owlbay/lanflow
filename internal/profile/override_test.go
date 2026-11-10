package profile

import (
	"strings"
	"testing"
)

func TestApplyOverride(t *testing.T) {
	base := `mixed-port: 7890
dns:
  enable: true
  nameserver:
    - 1.1.1.1
  fallback:
    - 8.8.8.8
rules:
  - DOMAIN,a.com,A
  - MATCH,B
proxy-groups:
  - name: A
    type: select
`
	tests := []struct {
		name     string
		override string
		want     string // exact output, or "" to use contains/err checks
		contains []string
		wantErr  string
	}{
		{
			name:     "replace scalar keeps position",
			override: "mixed-port: 1234\n",
			contains: []string{"mixed-port: 1234\ndns:"},
		},
		{
			name:     "new key appended at end",
			override: "ipv6: true\n",
			contains: []string{"    type: select\nipv6: true\n"},
		},
		{
			name:     "prepend to list",
			override: "+rules:\n  - DOMAIN,z.com,A\n",
			contains: []string{"rules:\n  - DOMAIN,z.com,A\n  - DOMAIN,a.com,A\n  - MATCH,B\n"},
		},
		{
			name:     "append to list",
			override: "rules+:\n  - DOMAIN,z.com,A\n",
			contains: []string{"rules:\n  - DOMAIN,a.com,A\n  - MATCH,B\n  - DOMAIN,z.com,A\n"},
		},
		{
			name:     "prepend creates missing list",
			override: "+listeners:\n  - name: x\n",
			contains: []string{"listeners:\n  - name: x\n"},
		},
		{
			name:     "replace list wholesale",
			override: "rules:\n  - MATCH,A\n",
			contains: []string{"rules:\n  - MATCH,A\nproxy-groups:"},
		},
		{
			name:     "nested map merges recursively",
			override: "dns:\n  ipv6: true\n  nameserver:\n    - 9.9.9.9\n",
			contains: []string{"dns:\n  enable: true\n  nameserver:\n    - 9.9.9.9\n  fallback:\n    - 8.8.8.8\n  ipv6: true\n"},
		},
		{
			name:     "nested list operators",
			override: "dns:\n  +nameserver: [0.0.0.0]\n  nameserver+: [2.2.2.2]\n",
			contains: []string{"  nameserver:\n    - 0.0.0.0\n    - 1.1.1.1\n    - 2.2.2.2\n"},
		},
		{
			name:     "bang replaces map wholesale",
			override: "dns!:\n  enable: false\n",
			contains: []string{"dns:\n  enable: false\nrules:"},
		},
		{
			name:     "comments ignored",
			override: "# comment\nmixed-port: 1 # trailing\n",
			contains: []string{"mixed-port: 1\n"},
		},
		{
			name:     "empty override returns base unchanged",
			override: "# only a comment\n",
			want:     base,
		},
		{
			name:     "prepend non-list value errors with line",
			override: "ipv6: true\n+rules: DOMAIN,a.com,A\n",
			wantErr:  "第 2 行",
		},
		{
			name:     "append to scalar errors",
			override: "mixed-port+: [1]\n",
			wantErr:  "是列表，但当前是标量",
		},
		{
			name:     "syntax error reports line",
			override: "dns:\n  enable: [\n",
			wantErr:  "YAML 语法错误",
		},
		{
			name:     "non-mapping override rejected",
			override: "- a\n- b\n",
			wantErr:  "键值映射",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ApplyOverride([]byte(base), tc.override)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got output:\n%s", tc.wantErr, out)
				}
				if !strings.Contains(err.Error(), tc.wantErr) || !strings.HasPrefix(err.Error(), "覆写") {
					t.Fatalf("error %q should start with 覆写 and contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want != "" && string(out) != tc.want {
				t.Fatalf("got:\n%s\nwant:\n%s", out, tc.want)
			}
			for _, c := range tc.contains {
				if !strings.Contains(string(out), c) {
					t.Fatalf("output missing %q:\n%s", c, out)
				}
			}
		})
	}
}

func TestApplyOverrideKeyOrderPreserved(t *testing.T) {
	base := "z: 1\ny: 2\nx: 3\n"
	out, err := ApplyOverride([]byte(base), "y: 20\nw: 4\n")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "z: 1\ny: 20\nx: 3\nw: 4\n" {
		t.Fatalf("order not preserved:\n%s", out)
	}
}

func TestApplyOverrideEmptyBase(t *testing.T) {
	out, err := ApplyOverride(nil, "a: 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "a: 1\n" {
		t.Fatalf("got %q", out)
	}
	_, err = ApplyOverride([]byte("- list\n"), "a: 1\n")
	if err == nil || !strings.Contains(err.Error(), "基础配置") {
		t.Fatalf("expected base error, got %v", err)
	}
}

func TestApplyOverrideMergeKey(t *testing.T) {
	// Values reached through a YAML merge key are readable and appendable.
	base := "common: &c\n  rules:\n    - MATCH,A\nsub:\n  <<: *c\n"
	out, err := ApplyOverride([]byte(base), "sub:\n  rules+:\n    - MATCH,B\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "sub:\n  <<: *c\n  rules:\n    - MATCH,A\n    - MATCH,B\n") {
		t.Fatalf("got:\n%s", out)
	}
	if !strings.Contains(string(out), "common: &c\n  rules:\n    - MATCH,A\nsub:") {
		t.Fatalf("anchor target must not be modified:\n%s", out)
	}
}
