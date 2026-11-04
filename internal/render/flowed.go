package render

import "strings"

// decodeFlowed undoes format=flowed (RFC 3676): lines ending in a space are
// soft breaks and join the next line of the same quote depth, so text sent
// from phones re-wraps to the window instead of breaking at 72 columns.
// Quoted lines come back as ">" per level and a space.
func decodeFlowed(s string, delSp bool) string {
	var out []string
	var para strings.Builder
	paraDepth := -1

	flush := func() {
		if paraDepth < 0 {
			return
		}
		out = append(out, quotePrefix(paraDepth)+para.String())
		para.Reset()
		paraDepth = -1
	}

	for _, line := range strings.Split(s, "\n") {
		depth := 0
		for depth < len(line) && line[depth] == '>' {
			depth++
		}
		content := line[depth:]
		content = strings.TrimPrefix(content, " ") // space-stuffing

		if paraDepth >= 0 && depth != paraDepth {
			flush() // quote depth changed: a flowed line can't continue across it
		}
		flowed := strings.HasSuffix(content, " ") && content != "-- "
		if flowed && delSp {
			content = content[:len(content)-1]
		}
		para.WriteString(content)
		paraDepth = depth
		if !flowed {
			flush()
		}
	}
	flush()
	return strings.Join(out, "\n")
}

func quotePrefix(depth int) string {
	if depth == 0 {
		return ""
	}
	return strings.Repeat(">", depth) + " "
}
