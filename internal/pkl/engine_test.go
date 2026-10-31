package pkl

import "testing"

func TestDeclaredEngine(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "docker",
			src: `config {
  engine = "docker"
}`,
			want: "docker",
		},
		{
			name: "podman",
			src: `config {
  engine = "podman"
}`,
			want: "podman",
		},
		{
			name: "ignores comments",
			src: `// engine = "docker"
/* engine = "podman" */
config {
  name = "web"
}`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeclaredEngine(tt.src); got != tt.want {
				t.Fatalf("DeclaredEngine() = %q, want %q", got, tt.want)
			}
		})
	}
}
