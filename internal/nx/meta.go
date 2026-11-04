package nx

import (
	"bytes"
	"io"
	"strings"
)

// readNACP — название игры и версия для показа из control.nacp.
func readNACP(n *NCA) (title, display string) {
	r, _, err := n.RomFS()
	if err != nil {
		return "", ""
	}
	fs, err := OpenRomFS(r)
	if err != nil {
		return "", ""
	}
	for _, f := range fs.Files {
		if f.Path != "control.nacp" {
			continue
		}
		b := make([]byte, 0x3070)
		if _, err := fs.Open(f).ReadAt(b, 0); err != nil && err != io.EOF {
			return "", ""
		}
		cstr := func(b []byte) string {
			if i := bytes.IndexByte(b, 0); i >= 0 {
				b = b[:i]
			}
			return strings.TrimSpace(string(b))
		}
		for i := range 16 {
			if title = cstr(b[i*0x300 : i*0x300+0x200]); title != "" {
				break
			}
		}
		return title, cstr(b[0x3060:0x3070])
	}
	return "", ""
}
