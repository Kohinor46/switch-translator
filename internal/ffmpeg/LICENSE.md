# ffmpeg

В программу вшиты статические сборки [FFmpeg](https://ffmpeg.org) (ffmpeg и ffprobe) — отдельные программы,
которые Switch Translator распаковывает и запускает. FFmpeg распространяется по лицензии GNU GPL 2 или новее
(эти сборки включают x264 и libvpx); исходники FFmpeg — https://ffmpeg.org/download.html.

Откуда сборки (см. `tools/fetch-ffmpeg.sh`):

- macOS: https://ffmpeg.martin-riedl.de (выпуски, проверяется sha256);
- Linux и Windows: https://github.com/BtbN/FFmpeg-Builds (выпуск n8.1, вариант gpl).

Исходники этих сборок и скрипты, которыми они собраны, опубликованы на тех же страницах.
