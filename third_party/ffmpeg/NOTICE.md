# FFmpeg redistribution notice

This application redistributes an unmodified, minimized build of FFmpeg 9.0.
FFmpeg is Copyright (c) 2000-2026 the FFmpeg developers and is licensed under
the GNU Lesser General Public License, version 2.1 or later.

The exact upstream source is `ffmpeg-9.0.tar.xz` from
<https://ffmpeg.org/releases/ffmpeg-9.0.tar.xz>, with SHA-256
`7f607a00dd0d28a729d5a4811205812eef01cf6ef6155025febb6f36a9062d52`.
Its detached signature is verified with FFmpeg release-signing fingerprint
`FCF986EA15E6E293A5644F10B4322F04D67658D8` before every build.

The corresponding supplemental signed Git tag is `n9.0`, commit
`d32b387f2b0a484599d4587d651891f0c63c4238`, verified during provenance
review with tag-signing fingerprint
`DD1EC9E8DE085C629B3E1846B18E8928B3948D64`.

No FFmpeg source files are modified. The build configuration is recorded in
`configure.flags`, the reproducible build procedure is in
`scripts/build-ffmpeg.ps1`, and `ffmpeg-build-config.txt` is distributed with
release materials. GPL and nonfree components are explicitly prohibited by
the build. The executable is compressed only with standard ZIP/DEFLATE; UPX is
not used.

Release materials include the exact source archive, detached signature, build
configuration, this notice, and the accompanying LGPL 2.1 license text. FFmpeg
source and project information are also available at <https://ffmpeg.org/>.
