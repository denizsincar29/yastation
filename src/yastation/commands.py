"""Команды станции, описанные через denizsincar29/func_parser.

Свободный текст без "/" уходит в default_command и просто озвучивается
станцией (это и есть "передать речь с компа на станцию").
Команды с "/" дают доступ к остальным возможностям клиента.
"""
from __future__ import annotations

from func_parser import CommandParser, arg

from .station_client import YandexStationClient

parser = CommandParser(prefix="/", hybrid=True)


def _client(ctx) -> YandexStationClient:
    client = ctx.get_var("client")
    if client is None:
        raise RuntimeError("станция ещё не подключена")
    return client


def _fmt(text: str) -> str:
    return text


# --- Свободный текст -> TTS -------------------------------------------------

@parser.default_command
async def on_text(ctx, content: str):
    client = _client(ctx)
    await client.say(content)
    return _fmt(f"[сказано] {content}")


# --- Явные команды -----------------------------------------------------------

@parser.command("say", aliases=["s", "tts"], help="Сказать текст через станцию (TTS)")
@arg("text", type=str, required=True, variadic=True, help="Что сказать")
async def cmd_say(ctx, text):
    client = _client(ctx)
    phrase = " ".join(text) if isinstance(text, list) else str(text)
    await client.say(phrase)
    return _fmt(f"[сказано] {phrase}")


@parser.command("cmd", aliases=["c"], help="Отправить произвольную голосовую команду Алисе")
@arg("text", type=str, required=True, variadic=True, help="Команда, как будто произнесённая голосом")
async def cmd_command(ctx, text):
    client = _client(ctx)
    phrase = " ".join(text) if isinstance(text, list) else str(text)
    await client.command(phrase)
    return _fmt(f"[команда отправлена] {phrase}")


@parser.command(
    "notify",
    aliases=["n"],
    help="Уведомление с заданной громкостью. Текст, потом опционально volume=0.3",
)
@arg("text", type=str, required=True, variadic=True, help="Текст уведомления")
@arg("volume", type=float, required=False, default=0.4, help="Громкость 0..1, задавать как volume=0.3")
async def cmd_notify(ctx, text, volume: float = 0.4):
    client = _client(ctx)
    phrase = " ".join(text) if isinstance(text, list) else str(text)
    await client.notify(phrase, volume=volume)
    return _fmt(f"[уведомление, громкость {volume}] {phrase}")


@parser.command("volume", aliases=["vol"], help="Установить громкость станции (0..1)")
@arg("level", type=float, required=True, help="Уровень громкости от 0 до 1")
async def cmd_volume(ctx, level: float):
    client = _client(ctx)
    await client.volume(level)
    return _fmt(f"[громкость] {level}")


@parser.command("play", help="Продолжить воспроизведение")
async def cmd_play(ctx):
    await _client(ctx).play()
    return _fmt("[play]")


@parser.command("pause", help="Пауза")
async def cmd_pause(ctx):
    await _client(ctx).pause()
    return _fmt("[pause]")


@parser.command("stop", help="Остановить")
async def cmd_stop(ctx):
    await _client(ctx).stop()
    return _fmt("[stop]")


@parser.command("next", help="Следующий трек")
async def cmd_next(ctx):
    await _client(ctx).next()
    return _fmt("[next]")


@parser.command("prev", aliases=["previous"], help="Предыдущий трек")
async def cmd_prev(ctx):
    await _client(ctx).previous()
    return _fmt("[previous]")


@parser.command("timer", help="Поставить таймер")
@arg("minutes", type=int, required=True, help="Через сколько минут")
@arg("text", type=str, required=False, default=None, variadic=True, help="Необязательная подпись")
async def cmd_timer(ctx, minutes: int, text=None):
    client = _client(ctx)
    label = " ".join(text) if isinstance(text, list) else text
    await client.timer(minutes, label)
    return _fmt(f"[таймер на {minutes} мин]" + (f" {label}" if label else ""))


@parser.command("alarm", help="Поставить будильник, например /alarm 7:30")
@arg("time", type=str, required=True, help="Время, например 7:30")
@arg("text", type=str, required=False, default=None, variadic=True, help="Необязательная подпись")
async def cmd_alarm(ctx, time: str, text=None):
    client = _client(ctx)
    label = " ".join(text) if isinstance(text, list) else text
    await client.alarm(time, label)
    return _fmt(f"[будильник на {time}]" + (f" {label}" if label else ""))


@parser.command("reminder", aliases=["remind"], help="/reminder <когда> <что напомнить>")
@arg("time", type=str, required=True, help="Когда, например 'завтра в 9'")
@arg("text", type=str, required=True, variadic=True, help="Что напомнить")
async def cmd_reminder(ctx, time: str, text):
    client = _client(ctx)
    phrase = " ".join(text) if isinstance(text, list) else str(text)
    await client.reminder(phrase, time)
    return _fmt(f"[напоминание {time}] {phrase}")


@parser.command("weather", help="Спросить погоду")
async def cmd_weather(ctx):
    await _client(ctx).weather()
    return _fmt("[погода запрошена]")


@parser.command("news", help="Включить новости")
async def cmd_news(ctx):
    await _client(ctx).news()
    return _fmt("[новости запрошены]")


@parser.command("scenarios", help="Список сценариев умного дома")
async def cmd_scenarios(ctx):
    client = _client(ctx)
    items = client.list_scenarios()
    if not items:
        return _fmt("сценариев не найдено")
    return "\n".join(f"- {i['name']}" for i in items)


@parser.command("scenario", aliases=["run"], help="Запустить сценарий по имени")
@arg("name", type=str, required=True, variadic=True, help="Имя сценария")
async def cmd_scenario(ctx, name):
    client = _client(ctx)
    scenario_name = " ".join(name) if isinstance(name, list) else str(name)
    await client.run_scenario(scenario_name)
    return _fmt(f"[сценарий запущен] {scenario_name}")


@parser.command("help", aliases=["h", "?"], help="Список команд")
async def cmd_help(ctx):
    return parser.help()


@parser.command("stations", aliases=["diag", "diagnostics"], help="Диагностика подключения и текущая станция")
async def cmd_diag(ctx):
    client = _client(ctx)
    info = client.diagnostics()
    lines = [
        f"станция по умолчанию: {info['default_station']['name']} ({info['default_station']['platform']})",
        f"колонок в аккаунте: {info['speakers_count']}",
        f"файл токена: {info['token_file']}",
    ]
    return "\n".join(lines)
