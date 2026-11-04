#!/usr/bin/env python3
"""Список файлов RomFS прямо из NSP/NSZ — без полной распаковки.

Читает только заголовки и таблицы RomFS, поэтому даже 10-гигабайтная игра
обрабатывается за секунды (NSZ без блочного сжатия — дольше: zstd-поток
приходится распаковывать до таблиц).

Использование:
    python3 switch_romfs_ls.py игра.nsp [игра2.nsz ...] [--json out.json]

Ключи берутся из ~/.switch/prod.keys и ~/.switch/title.keys.
Зависимости: pip install cryptography zstandard
"""
import argparse
import json
import os
import struct
import sys

import zstandard
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

CONTENT_TYPES = {0: "Program", 1: "Meta", 2: "Control", 3: "Manual", 4: "Data", 5: "PublicData"}
SIG_SIZES = {0x10000: 0x23C, 0x10001: 0x13C, 0x10002: 0x7C, 0x10003: 0x23C, 0x10004: 0x13C, 0x10005: 0x7C}


def load_keys(path):
    keys = {}
    if os.path.exists(path):
        for line in open(path, encoding="utf-8", errors="ignore"):
            if "=" in line:
                k, v = line.split("=", 1)
                keys[k.strip().lower()] = v.strip()
    return keys


PROD = load_keys(os.path.expanduser("~/.switch/prod.keys"))
TITLE = load_keys(os.path.expanduser("~/.switch/title.keys"))


def ecb_decrypt(key, data):
    d = Cipher(algorithms.AES(key), modes.ECB()).decryptor()
    return d.update(data) + d.finalize()


def decrypt_header(raw):
    hk = bytes.fromhex(PROD["header_key"])
    out = b""
    for i in range(len(raw) // 0x200):
        d = Cipher(algorithms.AES(hk), modes.XTS(i.to_bytes(16, "big"))).decryptor()
        out += d.update(raw[i * 0x200:(i + 1) * 0x200]) + d.finalize()
    return out


class Pfs0:
    """Контейнер NSP/NSZ: имя файла → (абсолютное смещение, размер)."""

    def __init__(self, path):
        self.f = open(path, "rb")
        head = self.f.read(16)
        if head[:4] != b"PFS0":
            raise ValueError("не PFS0 (не NSP/NSZ)")
        n, strsz = struct.unpack("<II", head[4:12])
        ents = [struct.unpack("<QQII", self.f.read(24)) for _ in range(n)]
        names = self.f.read(strsz)
        base = 16 + 24 * n + strsz
        self.files = {}
        for off, size, no, _ in ents:
            name = names[no:names.index(b"\0", no)].decode()
            self.files[name] = (base + off, size)

    def read(self, name, off, size):
        start, _ = self.files[name]
        self.f.seek(start + off)
        return self.f.read(size)


def titlekey_from_ticket(pfs, rights_id, gen):
    rid = rights_id.hex()
    enc = None
    for name, (_, size) in pfs.files.items():
        if not name.endswith(".tik"):
            continue
        tik = pfs.read(name, 0, size)
        data = SIG_SIZES.get(struct.unpack("<I", tik[:4])[0], 0x13C) + 4
        if tik[data + 0x160:data + 0x170].hex() == rid:
            enc = tik[data + 0x40:data + 0x50]
    if enc is None and rid in TITLE:
        enc = bytes.fromhex(TITLE[rid])
    if enc is None:
        raise ValueError(f"нет тикета/titlekey для rights id {rid}")
    kek = PROD.get(f"titlekek_{gen:02x}")
    if not kek:
        raise ValueError(f"нет titlekek_{gen:02x} — обнови ключи")
    return ecb_decrypt(bytes.fromhex(kek), enc)


class NcaPlain:
    """Чтение расшифрованных байтов NCA из NSP (AES-CTR по секциям)."""

    def __init__(self, pfs, name, hdr):
        self.pfs, self.name, self.hdr = pfs, name, hdr
        gen = max(hdr[0x206], hdr[0x220])
        gen = gen - 1 if gen else 0
        rights = hdr[0x230:0x240]
        if any(rights):
            self.key = titlekey_from_ticket(pfs, rights, gen)
        else:
            kak = PROD.get(f"key_area_key_application_{gen:02x}")
            if not kak:
                raise ValueError(f"нет key_area_key_application_{gen:02x}")
            self.key = ecb_decrypt(bytes.fromhex(kak), hdr[0x300:0x340])[0x20:0x30]

    def read(self, off, size, fs_header):
        enc_type = fs_header[4]
        start = off & ~0xF
        raw = self.pfs.read(self.name, start, size + (off - start) + 0xF & ~0xF)
        if enc_type == 1:  # без шифрования
            return raw[off - start:off - start + size]
        ctr = fs_header[0x140:0x148][::-1] + (start >> 4).to_bytes(8, "big")
        d = Cipher(algorithms.AES(self.key), modes.CTR(ctr)).decryptor()
        return (d.update(raw) + d.finalize())[off - start:off - start + size]


class NczPlain:
    """Чтение NCZ (NSZ): после 0x4000 байт идут уже расшифрованные данные в zstd."""

    def __init__(self, pfs, name):
        self.pfs, self.name = pfs, name
        p = 0x4000
        magic, count = struct.unpack("<8sQ", pfs.read(name, p, 16))
        if magic != b"NCZSECTN":
            raise ValueError("битый NCZ")
        p += 16 + count * 0x40
        self.body = p
        blk = pfs.read(name, p, 24)
        self.block = None
        if blk[:8] == b"NCZBLOCK":
            exp = blk[11]
            nblocks, dsize = struct.unpack("<IQ", blk[12:24])
            sizes = struct.unpack(f"<{nblocks}I", pfs.read(name, p + 24, 4 * nblocks))
            offs, o = [], p + 24 + 4 * nblocks
            for s in sizes:
                offs.append(o)
                o += s
            self.block = (1 << exp, sizes, offs, dsize)
        self.stream_pos = None

    def read(self, off, size, _fs_header=None):
        off -= 0x4000
        if self.block:
            bs, sizes, offs, dsize = self.block
            out = b""
            i = off // bs
            while len(out) < (off % bs) + size and i < len(sizes):
                chunk = self.pfs.read(self.name, offs[i], sizes[i])
                want = min(bs, dsize - i * bs)
                out += chunk if len(chunk) == want else zstandard.ZstdDecompressor().decompress(chunk, max_output_size=want)
                i += 1
            return out[off % bs:off % bs + size]
        # потоковый режим: разжимаем последовательно, читать можно только вперёд
        if self.stream_pos is None or off < self.stream_pos:
            self.pfs.f.seek(self.pfs.files[self.name][0] + self.body)
            self.reader = zstandard.ZstdDecompressor().stream_reader(self.pfs.f, read_across_frames=True)
            self.stream_pos = 0
        while self.stream_pos < off:
            skipped = len(self.reader.read(min(off - self.stream_pos, 64 << 20)))
            if not skipped:
                raise ValueError("NCZ закончился раньше времени")
            self.stream_pos += skipped
        data = self.reader.read(size)
        self.stream_pos += len(data)
        return data


def find_program(pfs):
    best = None
    for name, (_, size) in pfs.files.items():
        if not name.endswith((".nca", ".ncz")):
            continue
        hdr = decrypt_header(pfs.read(name, 0, 0xC00))
        if hdr[0x200:0x204] not in (b"NCA3", b"NCA2"):
            raise ValueError(f"{name}: заголовок не расшифровался — проверь header_key")
        if hdr[0x205] == 0 and (best is None or size > best[1]):
            best = (name, size, hdr)
    if not best:
        raise ValueError("нет Program NCA (это DLC/обновление/форвардер?)")
    return best


def list_romfs(path):
    pfs = Pfs0(path)
    name, _, hdr = find_program(pfs)
    # заголовок NCA начинается с подписей: 0x000 sig1, 0x100 sig2, 0x200 'NCA3'...
    h = hdr[0x200:0x400]
    title_id = struct.unpack("<Q", h[0x10:0x18])[0]
    reader = NczPlain(pfs, name) if name.endswith(".ncz") else NcaPlain(pfs, name, hdr)

    for i in range(4):
        start_mu, end_mu = struct.unpack("<II", h[0x40 + i * 0x10:0x48 + i * 0x10])
        fsh = hdr[0x400 + i * 0x200:0x600 + i * 0x200]
        if not end_mu or fsh[2] != 0 or fsh[3] != 3:  # нужен RomFS c IVFC
            continue
        if fsh[4] == 4:
            raise ValueError("RomFS в формате патча (BKTR) — нужна базовая версия")
        sec = start_mu * 0x200
        lvl_off = struct.unpack("<Q", fsh[0x8 + 0x10 + 5 * 0x18:0x8 + 0x10 + 5 * 0x18 + 8])[0]
        base = sec + lvl_off
        rh = struct.unpack("<10Q", reader.read(base, 0x50, fsh))
        _, dh_off, dh_sz, dm_off, dm_sz, fh_off, fh_sz, fm_off, fm_sz, data_off = rh
        lo = min(dm_off, fm_off)
        hi = max(dm_off + dm_sz, fm_off + fm_sz)
        tables = reader.read(base + lo, hi - lo, fsh)
        dmeta = tables[dm_off - lo:dm_off - lo + dm_sz]
        fmeta = tables[fm_off - lo:fm_off - lo + fm_sz]

        dirs, p = {}, 0
        while p + 0x18 <= len(dmeta):
            parent, _, _, _, _, nlen = struct.unpack("<6I", dmeta[p:p + 0x18])
            dirs[p] = (parent, dmeta[p + 0x18:p + 0x18 + nlen].decode("utf-8", "replace"))
            p += 0x18 + (nlen + 3 & ~3)

        def dir_path(o):
            parts = []
            while o in dirs and o != 0:
                parent, nm = dirs[o]
                parts.append(nm)
                o = parent
            return "/".join(reversed(parts))

        files, p = [], 0
        while p + 0x20 <= len(fmeta):
            parent, _, _, fsize, _, nlen = struct.unpack("<IIQQII", fmeta[p:p + 0x20])
            nm = fmeta[p + 0x20:p + 0x20 + nlen].decode("utf-8", "replace")
            d = dir_path(parent)
            files.append({"path": f"{d}/{nm}" if d else nm, "size": fsize})
            p += 0x20 + (nlen + 3 & ~3)
        return {"title_id": f"{title_id:016X}", "nca": name, "files": files}
    raise ValueError("в Program NCA нет RomFS")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("files", nargs="+")
    ap.add_argument("--json", help="сохранить списки файлов в JSON")
    args = ap.parse_args()
    if "header_key" not in PROD:
        sys.exit("Нет ~/.switch/prod.keys с header_key")

    result = {}
    for path in args.files:
        try:
            r = list_romfs(path)
            print(f"✅ {os.path.basename(path)}: {len(r['files'])} файлов, TitleID {r['title_id']}", flush=True)
        except Exception as e:
            r = {"error": str(e)}
            print(f"❌ {os.path.basename(path)}: {e}", flush=True)
        result[path] = r
    if args.json:
        with open(args.json, "w", encoding="utf-8") as out:
            json.dump(result, out, ensure_ascii=False)


if __name__ == "__main__":
    main()
