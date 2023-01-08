package tar

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//https://msdn.microsoft.com/en-us/library/windows/desktop/aa365247(v=vs.85).aspx
var reservedNames = [...]string{"CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9"}
var reservedCharsRegex *regexp.Regexp

const reservedCharsStr = `[<>:"\\|?*]`                    //NOTE: `/` is not included as it is our standard path separator
const reservedNamesRegexFmt = `(?i)^(%s)(?: *%s*)?[^\w ]` // $reservedName, $reservedCharsStr

func init() {
	reservedCharsRegex = regexp.MustCompile(reservedCharsStr)
}

func isNullDevice(path string) bool {
	if len(path) != 3 {
		return false
	}
	if path[0]|0x20 != 'n' {
		return false
	}
	if path[1]|0x20 != 'u' {
		return false
	}
	if path[2]|0x20 != 'l' {
		return false
	}
	return true
}

func sanitizePath(path string) (string, error) {
	pathElements := strings.Split(path, "/")

	//first pass: strip illegal tail & prefix reserved names `CON .` -> `_CON`
	for pi := range pathElements {
		pathElements[pi] = strings.TrimRight(pathElements[pi], ". ") //MSDN: Do not end a file or directory name with a space or a period

		for _, rn := range reservedNames {
			re, _ := regexp.Compile(fmt.Sprintf(reservedNamesRegexFmt, rn, reservedCharsStr)) //no err, regex is a constant with guaranteed constant arguments
			if matched := re.MatchString(pathElements[pi]); matched {
				pathElements[pi] = "_" + pathElements[pi]
				break
			}
		}
	}

	//second pass: scan and encode reserved characters ? -> %3F
	res := strings.Join(pathElements, `/`) //intentionally avoiding [file]path.Clean() being called with Join(); we do our own filtering first
	illegalIndices := reservedCharsRegex.FindAllStringIndex(res, -1)

	if illegalIndices != nil {
		var lastIndex int
		var builder strings.Builder
		allocAssist := (len(res) - len(illegalIndices)) + (len(illegalIndices) * 3) //3 = encoded length
		builder.Grow(allocAssist)

		for _, si := range illegalIndices {
			builder.WriteString(res[lastIndex:si[0]])              //append up to problem char
			builder.WriteString(url.QueryEscape(res[si[0]:si[1]])) //escape and append problem char
			lastIndex = si[1]
		}
		builder.WriteString(res[lastIndex:]) //append remainder
		res = builder.String()
	}

	return filepath.FromSlash(res), nil
}

func platformLink(inLink Link) error {
	if strings.HasPrefix(inLink.Target, string(os.PathSeparator)) || strings.HasPrefix(inLink.Target, "/") {
		return fmt.Errorf("Link target %q is relative to drive root (forbidden)", inLink.Target)
	}

	return nil
}
