# Сторонние компоненты

Этот каталог хранит исходные архивы, сведения о сборке, контрольные суммы и лицензионные материалы. Сами архивы не устанавливаются на станцию; пакет содержит собранные `bin/`, `lib/` и notices.

## Основные исходники

| Компонент | Версия | Источник |
|---|---|---|
| Shairport Sync | 5.6.2 | [Тег upstream](https://github.com/mikebrady/shairport-sync/tree/5.6.2) |
| NQPTP | 1.2.9 | [Тег upstream](https://github.com/mikebrady/nqptp/tree/1.2.9) |
| FFmpeg | 7.1.3 | [Исходный архив](https://ffmpeg.org/releases/ffmpeg-7.1.3.tar.xz) |
| Mbed TLS | 3.6.7 | [Релиз upstream](https://github.com/Mbed-TLS/mbedtls/releases/tag/mbedtls-3.6.7) |
| D-Bus | 1.16.2 | [Исходный архив](https://dbus.freedesktop.org/releases/dbus/dbus-1.16.2.tar.xz) |

Лицензии: Shairport Sync — GPL-3.0, NQPTP — GPL-2.0, выбранные компоненты FFmpeg — LGPL-2.1+, Mbed TLS — Apache-2.0 или GPL-2.0+, D-Bus — GPL-2.0+ или AFL-2.1. Сохраняйте конкретные notices из `licenses/` и `LICENSES/`; одинаковые тексты для разных архитектур хранятся в одном экземпляре.

[airplay2-build.json](airplay2-build.json) фиксирует dev-пакеты базовой сборки; [controls-runtime-build.json](controls-runtime-build.json) — входы control-enabled receiver. [SHA256SUMS.json](SHA256SUMS.json) фиксирует хеши бинарников, библиотек, notices и исходных архивов. Команды и патчи: [BUILD.md](../source/BUILD.md), `scripts/` и `support/`.

## Состав и ограничения

Старые Avahi 0.7 и часть ARMhf-библиотек переиспользуются из прежнего пакета. Их исходная воспроизводимость остаётся ограниченной: [THIRD_PARTY.md](../docs/THIRD_PARTY.md). Библиотеки прошивки — приватные входы связывания; их не следует распространять.

UPX 5.2.1 сжимает Go-супервизор и — только в архиве — runtime-бинарники (`--slim-runtime`). На станции декомпрессор не устанавливается; исходные `bin/` остаются несжатыми.

`bin/dbus-daemon`, `bin/dbus-send` и `lib/libdbus-1.so.3` собраны из D-Bus 1.16.2 в Debian bullseye: символы glibc ≤2.28, без RUNPATH. Они используют пакетные Expat/GCC runtime и штатную libc прошивки. `build-runtime.sh` пересобирает комплект с GCC и sysroot.

`shairport-sync-ap2` собирается из 5.6.3. На этапе линковки подключаются
`support/libmount-stub.c` (монтирования не перечисляются) и отключённые
контексты SELinux, поэтому `libmount`, `libblkid`, `libselinux` и `libpcre2`
не входят в пакет.

## Приёмник с shared-управлением

ARMhf receiver использует Shairport Sync 5.6.2 с патчами HomePod, Mbed TLS, shared-controls, DACP/IPv4 и preplay event channel из `support/`. `scripts/build-controls-runtime.sh` включает D-Bus-интерфейс и собирает его отдельно от базового receiver.

Дополнительные ARMhf-библиотеки avahi-client, blkid, config, mount, pcre2-8, popt, selinux, sodium и uuid происходят из проверенного Debian-набора, совместимого с прежним Avahi; `libplist-2.0.so.3` заменён на сборку bullseye (символы glibc ≤2.4). Их notices находятся в `LICENSES/controls-runtime`. `collect-runtime.py` переносит только недостающие публичные ELF-зависимости. `matching-resolver.py` относится к прежней схеме с пакетной libc и в 2.1.5 не используется.

Историческая сборка использовала кешированный Debian-образ. Полная чистая пересборка, закрепление исходников всех зависимостей и комплект для перелинковки остаются отдельными проверками перед бинарной публикацией. AArch64 shared-controls не квалифицированы. Не считайте наличие проверочных записей в JSON доказательством аппаратной приёмки нового runtime.
