package nx

import (
	"encoding/binary"
	"fmt"
)

type Ticket struct {
	RightsID     [16]byte
	EncTitleKey  [16]byte
	KeyGen       byte
	Personalized bool
}

func ParseTicket(data []byte) (*Ticket, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("тикет слишком короткий")
	}
	var sigLen int
	switch binary.LittleEndian.Uint32(data) {
	case 0x10000, 0x10003: // RSA-4096
		sigLen = 0x200 + 0x3C
	case 0x10001, 0x10004: // RSA-2048
		sigLen = 0x100 + 0x3C
	case 0x10002, 0x10005: // ECDSA
		sigLen = 0x3C + 0x40
	default:
		return nil, fmt.Errorf("неизвестный тип подписи тикета")
	}
	h := 4 + sigLen
	if len(data) < h+0x170 {
		return nil, fmt.Errorf("тикет слишком короткий")
	}
	t := &Ticket{
		KeyGen:       data[h+0x145],
		Personalized: data[h+0x141] != 0,
	}
	copy(t.EncTitleKey[:], data[h+0x40:])
	copy(t.RightsID[:], data[h+0x160:])
	return t, nil
}
