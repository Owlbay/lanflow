package profile

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"lanflow/internal/model"
)

func render(d []model.DiffLine) string {
	var sb strings.Builder
	for _, l := range d {
		sb.WriteByte(l.Op)
		sb.WriteString(l.Text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// reconstruct rebuilds the a and b sides from a diff.
func reconstruct(d []model.DiffLine) (a, b []string) {
	for _, l := range d {
		switch l.Op {
		case ' ':
			a = append(a, l.Text)
			b = append(b, l.Text)
		case '-':
			a = append(a, l.Text)
		case '+':
			b = append(b, l.Text)
		}
	}
	return
}

func TestDiff(t *testing.T) {
	cases := []struct{ name, a, b, want string }{
		{"identical", "a\nb\n", "a\nb\n", " a\n b\n"},
		{"empty both", "", "", ""},
		{"all added", "", "a\nb\n", "+a\n+b\n"},
		{"all removed", "a\nb\n", "", "-a\n-b\n"},
		{"insert middle", "a\nc\n", "a\nb\nc\n", " a\n+b\n c\n"},
		{"delete middle", "a\nb\nc\n", "a\nc\n", " a\n-b\n c\n"},
		{"replace", "a\nb\nc\n", "a\nx\nc\n", " a\n-b\n+x\n c\n"},
		{"classic", "a\nb\nc\na\nb\nb\na\n", "c\nb\na\nb\na\nc\n", "-a\n-b\n c\n+b\n a\n b\n-b\n a\n+c\n"},
		{"crlf", "a\r\nb\r\n", "a\nb\n", " a\n b\n"},
		{"no trailing newline", "a\nb", "a\nb\n", " a\n b\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := render(Diff([]byte(tc.a), []byte(tc.b)))
			if got != tc.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestDiffReconstructs(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 200; iter++ {
		n := rng.Intn(40)
		a := make([]string, n)
		for i := range a {
			a[i] = fmt.Sprint(rng.Intn(6))
		}
		b := append([]string(nil), a...)
		for k := rng.Intn(10); k > 0; k-- {
			switch rng.Intn(3) {
			case 0:
				if len(b) > 0 {
					i := rng.Intn(len(b))
					b = append(b[:i], b[i+1:]...)
				}
			case 1:
				i := rng.Intn(len(b) + 1)
				b = append(b[:i], append([]string{fmt.Sprint(rng.Intn(6))}, b[i:]...)...)
			case 2:
				if len(b) > 0 {
					b[rng.Intn(len(b))] = fmt.Sprint(rng.Intn(6))
				}
			}
		}
		join := func(s []string) []byte {
			if len(s) == 0 {
				return nil
			}
			return []byte(strings.Join(s, "\n") + "\n")
		}
		d := Diff(join(a), join(b))
		ra, rb := reconstruct(d)
		if strings.Join(ra, "\n") != strings.Join(a, "\n") || strings.Join(rb, "\n") != strings.Join(b, "\n") {
			t.Fatalf("iteration %d: diff does not reconstruct\na=%v\nb=%v\n%s", iter, a, b, render(d))
		}
	}
}

func TestDiffLargeFallsBackGracefully(t *testing.T) {
	var a, b []string
	for i := 0; i < maxDiffD+10; i++ {
		a = append(a, fmt.Sprintf("a%d", i))
		b = append(b, fmt.Sprintf("b%d", i))
	}
	d := Diff([]byte(strings.Join(a, "\n")), []byte(strings.Join(b, "\n")))
	ra, rb := reconstruct(d)
	if len(ra) != len(a) || len(rb) != len(b) {
		t.Fatalf("fallback diff lost lines: %d %d", len(ra), len(rb))
	}
}
