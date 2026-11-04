#!/usr/bin/env python3
"""Сканер распакованного RomFS игры Switch.

Определяет движок / middleware по сигнатурам файлов и показывает,
где лежат тексты и звук, и чем их открывать.

Использование:
    python3 scan_romfs.py путь/к/romfs [--json report.json] [--top 20]

Зависимости: только стандартная библиотека. Если установлен
`zstandard` (pip install zstandard), сканер заглядывает внутрь .zs-файлов.
"""
import argparse
import json
import os
import sys
from collections import defaultdict

try:
    import zstandard
except ImportError:
    zstandard = None

PEEK = 64
READ = 64 * 1024

# (сигнатура в начале файла, формат, категория)
MAGICS = [
    (b"MsgStdBn", "MSBT (тексты Nintendo LMS)", "text"),
    (b"MsgPrjBn", "MSBP (проект LMS: стили, теги)", "text"),
    (b"MsgFlwBn", "MSBF (ветки диалогов LMS)", "text"),
    (b"SARC", "SARC (архив Nintendo)", "archive"),
    (b"FSTM", "BFSTM (стрим-аудио Nintendo)", "audio"),
    (b"FWAV", "BFWAV (аудио Nintendo)", "audio"),
    (b"BWAV", "BWAV (аудио Nintendo)", "audio"),
    (b"BARS", "BARS (аудиоархив Nintendo)", "audio"),
    (b"BKHD", "Wwise SoundBank (.bnk)", "audio"),
    (b"AKPK", "Wwise File Package (.pck)", "audio"),
    (b"CPK ", "CRI CPK (архив CRIWARE)", "archive"),
    (b"@UTF", "CRI ACB / @UTF-таблица", "audio"),
    (b"AFS2", "CRI AWB (банк аудио)", "audio"),
    (b"HCA\x00", "CRI HCA (аудио)", "audio"),
    (b"\xc8\xc3\xc1\x00", "CRI HCA, зашифрованный", "audio"),
    (b"CRID", "CRI USM (видео)", "video"),
    (b"FSB5", "FMOD FSB5 (сэмплы)", "audio"),
    (b"UnityFS", "Unity AssetBundle", "archive"),
    (b"GDPC", "Godot PCK", "archive"),
    (b"RPA-", "Ren'Py RPA", "archive"),
    (b"OggS", "Ogg Vorbis/Opus", "audio"),
    (b"fLaC", "FLAC", "audio"),
    (b"ID3", "MP3", "audio"),
    (b"PK\x03\x04", "ZIP", "archive"),
    (b"BNTX", "BNTX (текстуры)", "graphics"),
    (b"FRES", "BFRES (модели)", "graphics"),
]

TEXT_EXTS = {".json", ".csv", ".tsv", ".txt", ".xml", ".ini", ".yaml", ".yml",
             ".lua", ".po", ".rpy", ".ks", ".scn", ".tbl", ".loc", ".lang", ".strings"}
UNITY_NAMES = {"globalgamemanagers", "data.unity3d", "mainData", "resources.assets"}
UNITY_EXTS = {".assets", ".ress", ".resource"}

TOOLS = {
    "Nintendo LMS / SARC": "Kuriimu2 или python: oead (SARC/Yaz0) + LMS/msbt-библиотеки; Switch Toolbox",
    "Unity": "AssetRipper, UABEA (UABE Avalonia), AssetStudio; тексты в TextAsset/MonoBehaviour (часто I2 Localization)",
    "Unreal Engine": "repak / FModel / UnrealPak (может понадобиться AES-ключ); тексты в Localization/*/*.locres → UE4LocalizationsTool",
    "CRIWARE": "YACpkTool / CriFsV2Lib для CPK; ACB/AWB/HCA → vgmstream (для HCA бывает нужен ключ)",
    "Wwise": "vgmstream (wem/bnk), bnkextr, quickbms + wwise_pck.bms",
    "FMOD": "vgmstream, python-fsb5, Fmod Bank Tools",
    "Godot": "GDRE Tools (Godot RE Tools)",
    "Ren'Py": "unrpa, unrpyc",
    "GameMaker": "UndertaleModTool (есть CLI)",
    "RPG Maker MV/MZ": "тексты прямо в data/*.json",
}


def human(n):
    for unit in ("Б", "КБ", "МБ", "ГБ"):
        if n < 1024:
            return f"{n:.0f} {unit}"
        n /= 1024
    return f"{n:.1f} ТБ"


def yaz0_peek(buf, want=PEEK):
    """Распаковывает только первые `want` байт Yaz0 — этого хватает для сигнатуры."""
    out = bytearray()
    src = 16
    while len(out) < want and src < len(buf):
        code = buf[src]
        src += 1
        for bit in range(8):
            if len(out) >= want or src >= len(buf):
                break
            if code & (0x80 >> bit):
                out.append(buf[src])
                src += 1
                continue
            if src + 1 >= len(buf):
                return bytes(out)
            b1, b2 = buf[src], buf[src + 1]
            src += 2
            dist = (((b1 & 0x0F) << 8) | b2) + 1
            n = b1 >> 4
            if n == 0:
                if src >= len(buf):
                    return bytes(out)
                n = buf[src] + 0x12
                src += 1
            else:
                n += 2
            if dist > len(out):
                return bytes(out)
            for _ in range(n):
                out.append(out[-dist])
    return bytes(out)


def zstd_peek(buf):
    if zstandard is None:
        return None
    try:
        return zstandard.ZstdDecompressor().decompressobj().decompress(buf)[:PEEK]
    except zstandard.ZstdError:
        return None


def match_magic(head, ext):
    for magic, fmt, cat in MAGICS:
        if head.startswith(magic):
            return fmt, cat
    if head.startswith(b"RIFF"):
        kind = head[8:12]
        if kind == b"WAVE":
            return ("Wwise WEM", "audio") if ext == ".wem" else ("WAV", "audio")
        if kind == b"FEV ":
            return "FMOD Bank (.bank)", "audio"
    if head.startswith(b"FORM") and head[8:12] == b"GEN8":
        return "GameMaker data.win", "archive"
    return None


def looks_like_text(buf):
    if buf.startswith((b"\xff\xfe", b"\xfe\xff")):
        return True
    sample = buf[:4096]
    if not sample:
        return False
    try:
        decoded = sample.decode("utf-8")
    except UnicodeDecodeError:
        # обрезанный многобайтный символ в конце сэмпла — не повод отказывать
        try:
            decoded = sample[:-3].decode("utf-8")
        except UnicodeDecodeError:
            return False
    printable = sum(ch.isprintable() or ch in "\r\n\t" for ch in decoded)
    return printable / max(len(decoded), 1) > 0.95


def has_unreal_pak_footer(path, size):
    # магия FPakInfo 0x5A6F12E1 лежит в последних ~200 байтах, смещение зависит от версии
    with open(path, "rb") as f:
        f.seek(max(0, size - 512))
        return b"\xe1\x12\x6f\x5a" in f.read()


def classify(path, name, ext, size):
    with open(path, "rb") as f:
        buf = f.read(READ)
    head = buf[:PEEK]

    wrapper = None
    if head.startswith(b"Yaz0"):
        wrapper, head = "Yaz0", yaz0_peek(buf)
    elif head.startswith(b"\x28\xb5\x2f\xfd"):
        wrapper = "zstd"
        inner = zstd_peek(buf)
        if inner is None:
            hint = "" if zstandard else " — pip install zstandard, чтобы заглянуть внутрь"
            return f"zstd-сжатый файл{hint}", "compressed"
        head = inner
    inner_ext = os.path.splitext(name[:-3])[1].lower() if wrapper and name.endswith(".zs") else ext

    found = match_magic(head, inner_ext)
    if found:
        fmt, cat = found
    elif ext in (".pak",) and has_unreal_pak_footer(path, size):
        fmt, cat = "Unreal PAK", "archive"
    elif ext in (".utoc", ".ucas"):
        fmt, cat = "Unreal IoStore (utoc/ucas)", "archive"
    elif ext == ".locres":
        fmt, cat = "Unreal .locres (локализация)", "text"
    elif name in UNITY_NAMES or ext in UNITY_EXTS or name.startswith("sharedassets"):
        fmt, cat = "Unity serialized/resource", "archive"
    elif not wrapper and (ext in TEXT_EXTS or looks_like_text(buf)):
        fmt, cat = f"текст {ext or '(без расширения)'}", "text"
    else:
        fmt, cat = f"неизвестно {inner_ext or '(без расширения)'}", "unknown"

    if wrapper:
        fmt += f" [{wrapper}]"
    return fmt, cat


def detect_engines(formats, paths):
    engines = {}
    fmts = " ".join(formats)
    lower_paths = [p.replace("\\", "/").lower() for p in paths]
    if "Unity" in fmts:
        engines["Unity"] = "найдены globalgamemanagers / *.assets / AssetBundle"
    if "Unreal" in fmts:
        engines["Unreal Engine"] = "найдены .pak / .utoc / .locres"
    if "MSBT" in fmts or "SARC" in fmts or "BFSTM" in fmts or "BWAV" in fmts:
        engines["Nintendo LMS / SARC"] = "найдены MSBT / SARC / BFSTM (стиль first-party Nintendo)"
    if "CRI" in fmts:
        engines["CRIWARE"] = "найдены CPK / ACB / AWB / HCA / USM"
    if "Wwise" in fmts:
        engines["Wwise"] = "найдены .bnk / .pck / .wem"
    if "FMOD" in fmts:
        engines["FMOD"] = "найдены FSB5 / .bank"
    if "Godot" in fmts:
        engines["Godot"] = "найден .pck с сигнатурой GDPC"
    if "Ren'Py" in fmts or any(p.endswith((".rpy", ".rpyc")) for p in lower_paths):
        engines["Ren'Py"] = "найдены .rpa / .rpy / .rpyc"
    if "GameMaker" in fmts:
        engines["GameMaker"] = "найден data.win"
    if any(p.endswith(("js/rpg_core.js", "js/rmmz_core.js")) for p in lower_paths):
        engines["RPG Maker MV/MZ"] = "найдены rpg_core.js / rmmz_core.js"
    return engines


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("root", help="папка с распакованным RomFS")
    ap.add_argument("--json", help="сохранить полный отчёт в JSON")
    ap.add_argument("--top", type=int, default=20, help="сколько крупнейших файлов показывать в каждой категории")
    args = ap.parse_args()

    if not os.path.isdir(args.root):
        sys.exit(f"Нет такой папки: {args.root}")

    files = []
    for dirpath, _, names in os.walk(args.root):
        for name in names:
            path = os.path.join(dirpath, name)
            try:
                size = os.path.getsize(path)
                fmt, cat = classify(path, name, os.path.splitext(name)[1].lower(), size)
            except OSError as e:
                fmt, cat, size = f"ошибка чтения: {e}", "unknown", 0
            files.append({"path": os.path.relpath(path, args.root), "size": size, "format": fmt, "category": cat})

    stats = defaultdict(lambda: [0, 0])
    for f in files:
        s = stats[(f["category"], f["format"])]
        s[0] += 1
        s[1] += f["size"]

    engines = detect_engines({f["format"] for f in files}, [f["path"] for f in files])

    print(f"Файлов: {len(files)}, всего {human(sum(f['size'] for f in files))}\n")
    print("== Движок / middleware ==")
    for name, why in engines.items() or [("не определён", "смотри таблицу форматов ниже")]:
        print(f"  • {name}: {why}")

    print("\n== Форматы ==")
    order = ["text", "audio", "video", "archive", "compressed", "graphics", "unknown"]
    for (cat, fmt), (count, size) in sorted(stats.items(), key=lambda kv: (order.index(kv[0][0]), -kv[1][1])):
        print(f"  {cat:<10} {count:>6} шт  {human(size):>9}  {fmt}")

    for cat, title in (("text", "Кандидаты на ТЕКСТ"), ("audio", "Кандидаты на ЗВУК")):
        picked = sorted((f for f in files if f["category"] == cat), key=lambda f: -f["size"])
        if not picked:
            continue
        print(f"\n== {title} (крупнейшие {min(args.top, len(picked))} из {len(picked)}) ==")
        for f in picked[:args.top]:
            print(f"  {human(f['size']):>9}  {f['path']}  — {f['format']}")

    if engines:
        print("\n== Чем открывать ==")
        for name in engines:
            if name in TOOLS:
                print(f"  • {name}: {TOOLS[name]}")

    if args.json:
        with open(args.json, "w", encoding="utf-8") as out:
            json.dump({"engines": engines, "files": files}, out, ensure_ascii=False, indent=2)
        print(f"\nПолный отчёт: {args.json}")


if __name__ == "__main__":
    main()
