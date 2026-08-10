# yastation

Скрипт для управления Яндекс Станцией с компьютера: озвучить текст,
отправить голосовую команду Алисе, поставить таймер/будильник, дернуть
сценарий умного дома и т.п. Свободный текст без `/` просто озвучивается
станцией.

Ядро запросов к Яндексу (`src/yastation/station_client.py`,
`src/yastation/auth_qr.py`) взято из [yasg1988/yandex-station-mcp](https://github.com/yasg1988/yandex-station-mcp)
и завёрнуто в свой CLI поверх твоего же
[func_parser](https://github.com/denizsincar29/func_parser).
Работает через облачный Quasar API (не через локальный Glagol WebSocket) —
станции не обязательно быть в той же сети, что и комп.

## Установка

```bash
uv sync
```

Если раньше `uv sync` не заводился в принципе — обычно дело либо в
недоступности github.com при первой синхронизации (она клонирует
func_parser и кэширует), либо в неверном build-backend. Здесь используется
родной `uv_build`, а не hatchling — он не требует лишних флагов для
git-зависимостей и не тянет hatchling как build-зависимость.

Если сети до github.com нет вообще, можно склонировать func_parser рядом
и заменить строку зависимости в `pyproject.toml` на
`"func-parser @ file:///путь/до/func_parser"`.

## Авторизация (один раз)

```bash
uv run yastation-auth
```

Скрипт покажет ссылку — открой её, войди под своим Яндекс-аккаунтом,
подтверди QR. После этого появится файл `src/yastation/yandex_tokens.json`
с токеном. **Не публикуй и не коммить этот файл.**

## Запуск

```bash
uv run yastation
```

Дальше:

```
> привет с компа!              # просто текст -> озвучивается станцией
> /volume 0.3                  # громкость 0..1
> /say выключи свет             # явный TTS
> /cmd включи радио Европа плюс # как будто сказано голосом
> /notify задача выполнена volume=0.4
> /timer 10 проверить духовку
> /alarm 7:30
> /reminder "завтра в 9" купить хлеб
> /play  /pause  /stop  /next  /prev
> /weather
> /news
> /scenarios                   # список сценариев умного дома
> /scenario Вечер              # запустить сценарий
> /stations                    # диагностика: какая станция сейчас дефолтная
> /help
```

## Если станций несколько

По умолчанию берётся первая. Задай через переменную окружения (можно в
`src/yastation/.env`):

```
YANDEX_STATION_ID=айди_станции
# или
YANDEX_STATION_NAME=Кухня
```

## Структура

```
pyproject.toml              uv-проект
src/yastation/
  station_client.py         вендоренный клиент Quasar/Glagol (yasg1988/yandex-station-mcp)
  auth_qr.py                QR-авторизация
  commands.py                команды на func_parser (say/cmd/notify/volume/...)
  __main__.py                REPL: python -m yastation / uv run yastation
```

## Лицензия исходного кода станции

У `yandex-station-mcp` нет явного файла лицензии в репозитории; в README
он упоминает, что использует подходы `AlexxIT/YandexStation` (MIT). Это
неофициальный API Яндекса, может сломаться в любой момент — это нормально,
не баг скрипта.
