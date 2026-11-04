// Package keys читает prod.keys и title.keys в формате Lockpick_RCM / hactool.
package keys

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

type Keyset struct {
	HeaderKey []byte // 32 байта: две половины ключа AES-XTS

	// key_area_key_{application,ocean,system}_XX, индекс — ревизия мастер-ключа.
	KeyAreaKeys [3]map[int][]byte
	Titlekeks   map[int][]byte

	// rights id → зашифрованный title key (как в common-тикете).
	TitleKeys map[[16]byte][]byte
}

var keyAreaPrefixes = [3]string{"key_area_key_application_", "key_area_key_ocean_", "key_area_key_system_"}

func Load(prodPath string) (*Keyset, error) {
	ks := &Keyset{
		Titlekeks: map[int][]byte{},
		TitleKeys: map[[16]byte][]byte{},
	}
	for i := range ks.KeyAreaKeys {
		ks.KeyAreaKeys[i] = map[int][]byte{}
	}
	err := readKeyFile(prodPath, func(name string, val []byte) {
		switch {
		case name == "header_key" && len(val) == 32:
			ks.HeaderKey = val
		case strings.HasPrefix(name, "titlekek_") && len(val) == 16:
			if rev, ok := revision(name, "titlekek_"); ok {
				ks.Titlekeks[rev] = val
			}
		default:
			for i, prefix := range keyAreaPrefixes {
				if strings.HasPrefix(name, prefix) && len(val) == 16 {
					if rev, ok := revision(name, prefix); ok {
						ks.KeyAreaKeys[i][rev] = val
					}
				}
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if ks.HeaderKey == nil {
		return nil, fmt.Errorf("в %s нет header_key — это точно prod.keys?", prodPath)
	}
	if len(ks.KeyAreaKeys[0]) == 0 {
		return nil, fmt.Errorf("в %s нет key_area_key_application_XX", prodPath)
	}
	return ks, nil
}

func (ks *Keyset) LoadTitleKeys(path string) error {
	return readKeyFile(path, func(name string, val []byte) {
		id, err := hex.DecodeString(name)
		if err != nil || len(id) != 16 || len(val) != 16 {
			return
		}
		var rid [16]byte
		copy(rid[:], id)
		ks.TitleKeys[rid] = val
	})
}

// Summary — короткое описание для GUI: какие ревизии мастер-ключей есть.
func (ks *Keyset) Summary() string {
	revs := make([]int, 0, len(ks.KeyAreaKeys[0]))
	for r := range ks.KeyAreaKeys[0] {
		revs = append(revs, r)
	}
	sort.Ints(revs)
	s := fmt.Sprintf("ключей области: %d (ревизии %02x–%02x), titlekek: %d", len(revs), revs[0], revs[len(revs)-1], len(ks.Titlekeks))
	if len(ks.TitleKeys) > 0 {
		s += fmt.Sprintf(", title keys: %d", len(ks.TitleKeys))
	}
	return s
}

func revision(name, prefix string) (int, bool) {
	n, err := strconv.ParseInt(strings.TrimPrefix(name, prefix), 16, 32)
	return int(n), err == nil
}

func readKeyFile(path string, fn func(name string, val []byte)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		name, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		b, err := hex.DecodeString(strings.TrimSpace(val))
		if err != nil {
			continue
		}
		fn(strings.ToLower(strings.TrimSpace(name)), b)
	}
	return sc.Err()
}
