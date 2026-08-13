# Протокол

Яндекс не публикует официальный API для управления Станцией с компа.
Общее понимание протокола (Passport QR-логин, облачный Quasar API со
сценариями-триггерами, локальный Glagol WebSocket) было взято из
нескольких открытых репозиториев про Яндекс Станцию — все они
недокументированные реверс-инжиниринговые проекты сообщества, официальной
спецификации не существует. Реализация здесь написана на Go самостоятельно.

Протокол недокументирован и может измениться в любой момент без
предупреждения — это ожидаемо, не баг библиотеки.

## Подтверждено на реальном аккаунте

- QR-логин: `passport.yandex.ru/pwl-yandex` -> CSRF из HTML ->
  `.../api/passport/auth/password/submit` -> `.../auth/magic/code` даёт
  `link` для QR -> поллинг `.../auth/magic/code/status` до
  `state == "otp_auth_finished"` -> `.../sessions/get_session`.
- Куки -> OAuth x-token: `POST
  https://mobileproxy.passport.yandex.net/1/bundle/oauth/token_by_sessionid`.
- Проверка/обновление токена: `.../1/bundle/account/short_info/`,
  `.../1/bundle/auth/x_token/`.
- CSRF для iot.quasar.yandex.ru: `GET https://yandex.ru/quasar`,
  `"csrfToken2":"..."` из HTML, заголовок `x-csrf-token`; при 403 — взять
  заново.
- Список устройств: `GET https://iot.quasar.yandex.ru/m/v3/user/devices`
  -> `households[].all[]` (плоский список, без вложенности по комнатам).
  Колонка — это устройство с `quasar_info` и непустым `capabilities`.
- Список сценариев: `GET .../m/user/scenarios`; создание — `POST
  .../m/v4/user/scenarios` (id в ответе — `"scenario_id"` на верхнем
  уровне); правка — `PUT .../m/v4/user/scenarios/{id}`; запуск без
  произнесения фразы — `POST .../m/user/scenarios/{id}/actions`.
- Устройство внутри тела сценария (`items[].id`, `items[].value.id`)
  адресуется собственным `id` устройства (тем же, что и в `households[].all[].id`),
  а не `quasar_info.device_id` — это отдельный, «железный» идентификатор,
  который используется только Glagol.
- Форма триггера: `{"trigger": {"type": "scenario.trigger.voice", "value": "<фраза>"}}`.
  `value` не всегда строка — у нестандартных (не голосовых) сценариев,
  созданных официальным приложением, это может быть объект; такие триггеры
  просто пропускаются при поиске своего сценария.
- Форма шага: `"scenarios.steps.actions.v2"`, `parameters.items[]`, каждый
  item — `{"id", "type": "step.action.item.device", "value": {"id",
  "item_type": "device", "capabilities": [...]}}`.
- TTS-капабилити: `"devices.capabilities.quasar"`, `instance: "tts"`,
  `value: {"text": "..."}`.
- Капабилити голосовой команды: `"devices.capabilities.quasar.server_action"`,
  `instance: "text_action"`, `value` — строка с текстом команды.
- `encode_uid`: фраза-триггер — hex-символы id устройства, посимвольно
  переведённые по фиксированной маске `0123456789abcdef-` ->
  `оеаинтсрвлкмдпуяы`.
- Capabilities реального устройства (колонка с подсветкой), полученные
  через `Client.Capabilities()`/`/capabilities` — подтверждают, что
  `devices.capabilities.quasar` несёт куда больше инстансов, чем
  `tts`/`text_action`, использованные раньше:
  - `tts`: значение `{"text","lang","whisper"}` — есть отдельное булево
    поле `whisper`, а не только текст. Никакого SSML тут нет и не нужно —
    шёпот включается этим флагом (`Client.SayWhisper`, `/whisper`), а не
    фразой "скажи шёпотом" и не разметкой в тексте.
  - `sound_play`: значение `{"sound", "sound_name"}` — библиотека звуковых
    эффектов Алисы по id (пример: `chainsaw-1` / "Бензопила"). Полного
    каталога id нет; практический способ найти конкретный — попросить
    Алису голосом, потом прочитать `sound_play.state.value.sound` через
    `/capabilities`.
  - `stop_everything`: значение `{}` — более жёсткий стоп, чем обычная
    команда "останови".
  - `weather`/`news`/`music_play`: структурные аналоги существующих
    голосовых команд; `news` несёт `topic`/`topic_name` — то есть можно
    выбрать конкретную тему новостей, не только "главное" (набор тем не
    подтверждён).
  - `server_action`/`phrase_action` — отдельный инстанс рядом с уже
    используемым `text_action`. Назначение не выяснено, не реализовано.
  - `devices.capabilities.color_setting`/`scene` (на устройствах с
    подсветкой): значение `{"id","name"}`, список сцен приходит в
    `parameters.scenes` (пример: `lava_lamp`, `night`, `candle`,
    `inactive`). `Client.LightScene`/`/scene` шлют `{"id": ...}` в этот
    инстанс — **не независимо подтверждено**, что этого набора полей
    достаточно для записи (только чтение текущего состояния подтверждено
    напрямую).
  - `alice_show`: значение `{}`, назначение не выяснено, не реализовано.

## Не подтверждено на реальном железе

- **Glagol** (`internal/glagol`): токен через `quasar.yandex.net/glagol/token`
  и сам факт локального WSS с самоподписанным сертификатом — в этом
  уверен. Форма сообщения (`conversationToken`/`id`/`sentTime`/
  `payload.command: "sendText"`) и то, что отдельной команды на "чистый
  TTS" в локальном протоколе, похоже, не существует — не проверено на
  живой колонке. Если `/say` через Glagol поведёт себя не так, как
  ожидаешь — смотри сюда в первую очередь.
- Остальной код (планировщик, диспетчер команд, очередь, HTTP-сервер,
  скрипты, пользовательские команды) не завязан на детали протокола
  Яндекса и полностью покрыт тестами.
