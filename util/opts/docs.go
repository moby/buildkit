package opts

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

func GenMarkdown(opts []OptInfo) string {
	w := bytes.NewBuffer(nil)
	genMarkdown(w, opts, "")
	return w.String()
}

func genMarkdown(w io.Writer, opts []OptInfo, prefix string) {
	for _, opt := range opts {
		if opt.Hidden {
			continue
		}

		if opt.Key != "" || opt.Name != "" || opt.Help != "" {
			hasContent := false
			fmt.Fprint(w, prefix+"- ")
			if opt.Name != "" {
				fmt.Fprint(w, opt.Name)
				hasContent = true
			}

			if opt.Key != "" {
				if hasContent {
					fmt.Fprint(w, " ")
				}

				choices := ""
				if len(opt.Choices) > 1 {
					choices = strings.Join(opt.Choices, "|")
				} else if opt.Type == "bool" {
					choices = "true|false"
				} else if opt.Type != "" {
					choices = opt.Type
				}
				if choices == "" {
					fmt.Fprintf(w, "`%s`", opt.Key)
				} else {
					fmt.Fprintf(w, "`%s=<%s>`", opt.Key, choices)
				}
				hasContent = true
			}
			if opt.Default != nil {
				s := fmt.Sprint(opt.Default)
				if s != "" {
					fmt.Fprintf(w, " (default %q)", s)
				}
			}

			if hasContent {
				fmt.Fprint(w, "\n\n")
			}

			if opt.Help != "" {
				fmt.Fprint(w, prefix+indent(opt.Help, "  ")+"\n\n")
			}
		}

		if opt.Children != nil {
			if opt.Name == "" && opt.Help == "" {
				genMarkdown(w, opt.Children, prefix)
			} else {
				genMarkdown(w, opt.Children, prefix+"  ")
			}
		}
	}
}

func indent(s string, with string) string {
	lines := strings.SplitAfter(s, "\n")
	for i, line := range lines {
		if len(line) == 1 {
			continue
		}
		lines[i] = with + line
	}
	return strings.Join(lines, "")
}
