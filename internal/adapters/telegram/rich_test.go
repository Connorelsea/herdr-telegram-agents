package telegram

import "testing"

func TestEscapeRich(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"prose", "echo $PATH and $HOME, List<T> & <div>", `echo \$PATH and \$HOME, List&lt;T> &amp; &lt;div>`},
		{"spoiler and mark", "a ||b|| and ==c==", `a \|\|b\|\| and \=\=c\=\=`},
		{"markdown kept", "## Head\n- **bold** *it* [x](https://t.me/a_b)\n> quote", "## Head\n- **bold** *it* [x](https://t.me/a_b)\n> quote"},
		{"inline code", "run `a || b $X <T>` then $Y", "run `a || b $X <T>` then \\$Y"},
		{"double ticks", "``a ` $b`` $c", "``a ` $b`` \\$c"},
		{"unmatched tick", "a ` $b", "a ` \\$b"},
		{"fence", "before $a\n```sh\necho $HOME || true\n```\nafter $b", "before \\$a\n```sh\necho $HOME || true\n```\nafter \\$b"},
		{"unclosed fence", "```\n$x <y>", "```\n$x <y>"},
		{"tilde fence", "~~~\n$x\n~~~\n$y", "~~~\n$x\n~~~\n\\$y"},
		// A row keeps its || (an empty cell); a blank line ends the table.
		{"table", "| a | b |\n|---|:-:|\n| $1 || <T> |\n\nafter || x", "| a | b |\n|---|:-:|\n| \\$1 || &lt;T> |\n\nafter \\|\\| x"},
		{"pipes without rule", "a | b\nc | $d", "a | b\nc | \\$d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeRich(tc.in); got != tc.want {
				t.Fatalf("escapeRich(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRichFooter(t *testing.T) {
	if got := richFooter(""); got != "" {
		t.Fatalf("empty footer = %q", got)
	}
	if got := richFooter("⏱ 2m · $3 <x>"); got != "\n\n⏱ 2m · \\$3 &lt;x>" {
		t.Fatalf("footer = %q", got)
	}
}
