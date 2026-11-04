package nx

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
)

// Nintendo использует AES-128-XTS с номером сектора в big-endian,
// поэтому golang.org/x/crypto/xts (little-endian tweak) не подходит.
type ntXTS struct {
	k1, k2 cipher.Block
}

func newXTS(key []byte) *ntXTS {
	k1, _ := aes.NewCipher(key[:16])
	k2, _ := aes.NewCipher(key[16:32])
	return &ntXTS{k1, k2}
}

func (x *ntXTS) crypt(buf []byte, sector uint64, sectorSize int, decrypt bool) {
	for off := 0; off+sectorSize <= len(buf); off += sectorSize {
		var tweak [16]byte
		binary.BigEndian.PutUint64(tweak[8:], sector)
		x.k2.Encrypt(tweak[:], tweak[:])
		for b := off; b < off+sectorSize; b += 16 {
			blk := buf[b : b+16]
			xor16(blk, tweak[:])
			if decrypt {
				x.k1.Decrypt(blk, blk)
			} else {
				x.k1.Encrypt(blk, blk)
			}
			xor16(blk, tweak[:])
			gfMul2(&tweak)
		}
		sector++
	}
}

func xor16(dst, src []byte) {
	for i := 0; i < 16; i++ {
		dst[i] ^= src[i]
	}
}

func gfMul2(t *[16]byte) {
	var carry byte
	for i := 0; i < 16; i++ {
		next := t[i] >> 7
		t[i] = t[i]<<1 | carry
		carry = next
	}
	if carry != 0 {
		t[0] ^= 0x87
	}
}

func ecbDecrypt(key, data []byte) []byte {
	c, _ := aes.NewCipher(key)
	out := make([]byte, len(data))
	for i := 0; i+16 <= len(data); i += 16 {
		c.Decrypt(out[i:i+16], data[i:i+16])
	}
	return out
}

func ecbEncrypt(key, data []byte) []byte {
	c, _ := aes.NewCipher(key)
	out := make([]byte, len(data))
	for i := 0; i+16 <= len(data); i += 16 {
		c.Encrypt(out[i:i+16], data[i:i+16])
	}
	return out
}

// ctrIV собирает счётчик секции: старшие 8 байт из FS-заголовка,
// младшие — абсолютное смещение в NCA / 16 (big-endian).
func ctrIV(sectionCtr [8]byte, absOffset int64) []byte {
	iv := make([]byte, 16)
	copy(iv, sectionCtr[:])
	binary.BigEndian.PutUint64(iv[8:], uint64(absOffset)>>4)
	return iv
}
