package pkl

import "testing"

func TestWithStackName(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "replaces starter placeholder",
			src:  "config {\n  name   = \"my-app\"\n  services {\n  }\n}\n",
			want: "config {\n  name   = \"immich\"\n  services {\n  }\n}\n",
		},
		{
			name: "keeps an explicit name",
			src:  "config {\n  name = \"custom\"\n}\n",
			want: "config {\n  name = \"custom\"\n}\n",
		},
		{
			name: "inserts when missing",
			src:  "config {\n  services {\n  }\n}\n",
			want: "config {\n  name = \"immich\"\n\n  services {\n  }\n}\n",
		},
		{
			name: "ignores name in nested blocks",
			src:  "config {\n  services {\n    [\"web\"] {\n      labels {\n        name = \"x\"\n      }\n    }\n  }\n}\n",
			want: "config {\n  name = \"immich\"\n\n  services {\n    [\"web\"] {\n      labels {\n        name = \"x\"\n      }\n    }\n  }\n}\n",
		},
		{
			name: "ignores braces in strings and comments",
			src:  "// config { name = \"fake\" }\nconfig {\n  name = \"my-app\"\n  services { [\"web\"] { command = List(\"sh\", \"-c\", \"echo }\") } }\n}\n",
			want: "// config { name = \"fake\" }\nconfig {\n  name = \"immich\"\n  services { [\"web\"] { command = List(\"sh\", \"-c\", \"echo }\") } }\n}\n",
		},
		{
			name: "no config block",
			src:  "services { }\n",
			want: "services { }\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WithStackName(tc.src, "immich"); got != tc.want {
				t.Errorf("WithStackName() =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestDeclaredName(t *testing.T) {
	src := "config {\n  name = \"my-app\"\n  services { }\n}\n"
	if got := DeclaredName(src); got != "my-app" {
		t.Errorf("DeclaredName() = %q, want my-app", got)
	}
	if got := DeclaredName("config { services { } }"); got != "" {
		t.Errorf("DeclaredName() = %q, want empty", got)
	}
}
